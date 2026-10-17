// SPDX-License-Identifier: Apache-2.0
// The native pause model: functions that may pause are async functions, and
// each task's body runs as a Swift task on its owner's executor. Only the
// owner's scheduler resumes a task, so Goalchemy, not Swift, decides which
// task runs next; Swift's cooperative pool never runs source code.
import Foundation

#if os(Linux)
  import Glibc
#else
  import Darwin
#endif

typealias GAsyncBody = (GTask, [GValue], [GCell]) async throws -> [GValue]

/// GAsyncFrame is the state of a frame whose body is an async function.
final class GAsyncFrame {
  let body: (GTask) async throws -> [GValue]
  var started = false
  var finished = false
  var results: [GValue] = []
  var failure: Error?
  init(_ body: @escaping (GTask) async throws -> [GValue]) { self.body = body }
}

/// GExit unwinds a parked task body at shutdown without running source defers.
struct GExit: Error {}

/// GHandoff says why the turn returned to the driver.
enum GHandoff {
  case idle, returned
  case failed(Error)
}

/// GExecutor runs the jobs of an owner's task bodies on the owner thread, one
/// at a time, in the order the scheduler resumes them.
@available(macOS 15.0, iOS 18.0, tvOS 18.0, watchOS 11.0, visionOS 2.0, *)
final class GExecutor: SerialExecutor, TaskExecutor, @unchecked Sendable {
  private let thread = pthread_self()
  private var local: [UnownedJob] = []
  private var head = 0
  private let remote = NSCondition()
  private var foreign: [UnownedJob] = []
  func enqueue(_ job: consuming ExecutorJob) {
    let j = UnownedJob(job)
    if pthread_equal(thread, pthread_self()) != 0 {
      local.append(j)
      return
    }
    remote.lock()
    foreign.append(j)
    remote.signal()
    remote.unlock()
  }
  func asUnownedSerialExecutor() -> UnownedSerialExecutor { UnownedSerialExecutor(ordinary: self) }
  func runOne() {
    let j: UnownedJob
    if head < local.count {
      j = local[head]
      head += 1
      if head == local.count {
        local.removeAll(keepingCapacity: true)
        head = 0
      }
    } else {
      remote.lock()
      while foreign.isEmpty { remote.wait() }
      j = foreign.removeFirst()
      remote.unlock()
    }
    j.runSynchronously(isolatedTo: asUnownedSerialExecutor(), taskExecutor: asUnownedTaskExecutor())
  }
}

extension GOwner {
  /// start runs a task body as a Swift task that prefers this owner's
  /// executor, so its jobs, including every resumption, run on this thread.
  func start(_ task: GTask, _ n: GAsyncFrame) throws {
    guard #available(macOS 15.0, iOS 18.0, tvOS 18.0, watchOS 11.0, visionOS 2.0, *) else {
      throw GFault(
        "the native pause model requires macOS 15, iOS 18, tvOS 18, watchOS 11 or visionOS 2")
    }
    let ex: GExecutor
    if let e = executor as? GExecutor {
      ex = e
    } else {
      ex = GExecutor()
      executor = ex
    }
    Task(executorPreference: ex) {
      do {
        n.results = try await n.body(task)
      } catch is GExit {
        task.native = nil
        self.back = .returned
        return
      } catch {
        n.failure = error
      }
      n.finished = true
      self.finish(task)
    }
  }
  /// finish delivers a finished task body like a frame return, then continues
  /// the task's remaining frames and the scheduler.
  private func finish(_ task: GTask) {
    task.native = nil
    do {
      try drive(task)
      if away {
        away = false
        return
      }
    } catch {
      back = .failed(error)
      return
    }
    _ = turn(nil)
  }
  /// turn runs the scheduler for a task body that blocked or finished (me
  /// nil) while holding the turn, dispatching the next task directly. It
  /// reports true when me is next. The driver takes the turn back when no
  /// task is runnable, when main is done, and on scheduler failures, which it
  /// raises itself, so they never unwind a task body.
  func turn(_ me: GTask?) -> Bool {
    do {
      while true {
        if main?.done != false {
          back = .returned
          return false
        }
        guard let u = poll() else {
          back = .idle
          return false
        }
        if u === me { return true }
        try drive(u)
        if away {
          away = false
          return false
        }
      }
    } catch {
      back = .failed(error)
      return false
    }
  }
  /// turnBack runs task bodies until one passes the turn back to the driver.
  func turnBack() throws {
    away = false
    let h = runJobs()
    switch h {
    case .failed(let error): throw error
    case .idle: if main?.done == false { try idle() }
    case .returned: break
    }
  }
  private func runJobs() -> GHandoff {
    back = nil
    while back == nil {
      if #available(macOS 15.0, iOS 18.0, tvOS 18.0, watchOS 11.0, visionOS 2.0, *) {
        (executor as! GExecutor).runOne()
      }
    }
    defer { back = nil }
    return back!
  }
  /// unwindNative resumes each parked task body with GExit, one at a time,
  /// so its async frames are freed without running source defers.
  func unwindNative() {
    for task in tasks {
      guard let c = task.cont else { continue }
      task.cont = nil
      c.resume(throwing: GExit())
      _ = runJobs()
    }
  }
}

extension GTask {
  /// pause completes a pause primitive's action: results arrive at once in
  /// rv, or the task waits for the scheduler to choose it again.
  func pause(_ action: GAction) async throws {
    switch action {
    case .resume(let values): rv = values
    case .yield:
      owner.ready(self)
      try await wait()
    case .park:
      blocked = true
      try await wait()
    default: throw GFault("invalid pause action")
    }
  }
  /// wait gives up the turn until the scheduler chooses this task again,
  /// then raises a panic the waker left for it.
  func wait() async throws {
    try await park()
    if let p = resumePanic {
      resumePanic = nil
      throw p
    }
  }
  private func park() async throws {
    if owner.turn(self) { return }
    try await withUnsafeThrowingContinuation { (c: UnsafeContinuation<Void, Error>) in cont = c }
  }
  /// call runs a frame on this task's body and returns its results: frames
  /// are driven until they pause or finish, and a native frame's body is
  /// awaited directly.
  func call(_ frame: GFrame) async throws -> [GValue] {
    let base = stack.count
    stack.append(frame)
    do {
      while true {
        try owner.drive(self, base: base, inline: true)
        if stack.count == base { return rv }
        if let n = stack.last?.native, !n.started {
          n.started = true
          do { n.results = try await n.body(self) } catch let p as GPanic { n.failure = p }
          n.finished = true
          continue
        }
        try await park()
      }
    } catch {
      if stack.count > base { stack.removeSubrange(base...) }
      throw error
    }
  }
  /// enter pushes the frame that holds an async function's locals as
  /// collector roots, and its deferred calls and panic.
  func enter(_ id: Int, _ roots: @escaping () -> [GValue]) -> GFrame {
    let f = GFrame(id)
    f.roots = roots
    stack.append(f)
    return f
  }
  func leave(_ frame: GFrame) {
    if stack.last === frame {
      stack.removeLast()
    } else if let i = stack.lastIndex(where: { $0 === frame }) {
      stack.removeSubrange(i...)
    }
  }
  /// runDefers runs an async function's deferred calls, awaiting those that
  /// pause; recover and the panic chain behave as for frames.
  func runDefers(_ frame: GFrame) async throws {
    while let d = frame.defers.popLast() {
      let savedPanic = currentPanic
      let savedTarget = deferTarget
      currentPanic = frame.panic
      deferTarget = d.id
      do {
        _ = try await call(d)
      } catch let p as GPanic {
        currentPanic = savedPanic
        deferTarget = savedTarget
        if p !== frame.panic, p.previous == nil { p.previous = frame.panic }
        frame.panic = p
        continue
      }
      currentPanic = savedPanic
      deferTarget = savedTarget
      if frame.panic?.recovered == true { frame.panic = nil }
    }
    if let p = frame.panic { throw p }
  }
}
