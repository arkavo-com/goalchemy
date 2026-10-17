// SPDX-License-Identifier: Apache-2.0
import Foundation

#if os(Linux)
  import Glibc
#else
  import Darwin
#endif

struct GFault: Error {
  let message: String
  init(_ message: String) { self.message = message }
}
struct GFatal: Error {
  let message: String
  init(_ message: String) { self.message = message }
}
final class GPanic: Error {
  let value: GValue
  var recovered = false
  var previous: GPanic?
  init(_ value: GValue) { self.value = value }
  static func runtime(_ text: String, _ type: String = "runtime.errorString") -> GPanic {
    GPanic(
      .interface(
        GInterface(-1, .error(GError("runtime error: " + text, type, false)))))
  }
  static func plain(_ text: String) -> GPanic {
    GPanic(.interface(GInterface(-1, .error(GError(text, "runtime.plainError", false)))))
  }
  static func string(_ text: String) -> GPanic {
    let t =
      GTypes.table.firstIndex { $0.name == "string" }
      ?? GNative.addPrimitive("string", "string", 0, false)
    return GPanic(.interface(GInterface(t, .text(text))))
  }
  static func source(_ value: GValue) -> GPanic {
    if case .nilValue = value {
      return GPanic(
        .interface(
          GInterface(
            -1,
            .error(
              GError("runtime error: panic called with nil argument", "*runtime.PanicNilError")))
        ))
    }
    return GPanic(value)
  }
}
func GPanicBytes(_ panic: GPanic) -> [UInt8] {
  var prior = panic.previous.map { GPanicBytes($0) + [9] } ?? []
  var bytes = GFormat(panic.value)
  if case .interface(let box) = panic.value, box.type >= 0 {
    let t = GTypes.table[box.type]
    let method =
      (t.nativeMethods.contains("Error") ? t.methods["Error"] : nil)
      ?? (t.nativeMethods.contains("String") ? t.methods["String"] : nil)
    if let method = method {
      if let result = try? GOwner(host: false).run(method.start([box.value])) {
        bytes = result.first?.bytes ?? []
      }
    } else if t.name.contains(".") && ["int", "bool", "float", "string"].contains(t.kind) {
      bytes =
        Array((t.name + "(").utf8) + (t.kind == "string" ? [34] : []) + bytes
        + (t.kind == "string" ? [34] : []) + [41]
    }
  }
  prior += Array("panic: ".utf8)
  for byte in bytes {
    prior.append(byte)
    if byte == 10 { prior.append(9) }
  }
  if panic.recovered { prior += Array(" [recovered]".utf8) }
  return prior + [10]
}
func GPanicText(_ panic: GPanic) -> String { String(decoding: GPanicBytes(panic), as: UTF8.self) }

/// GMain runs an executable's entry on a thread with a 1 GiB stack, as Go
/// allows: functions that cannot pause recurse on the native stack.
func GMain(_ body: @escaping () -> Void) {
  let done = DispatchSemaphore(value: 0)
  let thread = Thread {
    body()
    done.signal()
  }
  thread.stackSize = 1 << 30
  thread.start()
  done.wait()
}

func GReport(_ error: Error) -> Never {
  let text: String
  if let p = error as? GPanic {
    FileHandle.standardError.write(Data(GPanicBytes(p)))
    exit(2)
  } else if let fatal = error as? GFatal {
    text = "fatal error: " + fatal.message + "\n"
  } else if let f = error as? GFault {
    text = "goalchemy fault: " + f.message + "\n"
  } else {
    text = "goalchemy host fault: \(error)\n"
  }
  FileHandle.standardError.write(Data(text.utf8))
  exit(2)
}

final class GFunction: GManaged {
  let id: Int
  private var maker: (([GValue], [GCell]) -> GFrame)?
  /// native is the async entry of a function of the native pause model.
  let native: GAsyncBody?
  var environment: [GCell]
  var retained: [GValue] = []
  private var receiver: GValue?
  init(
    _ id: Int, _ maker: @escaping ([GValue], [GCell]) -> GFrame, _ environment: [GCell] = [],
    native: GAsyncBody? = nil
  ) {
    self.id = id
    self.maker = maker
    self.native = native
    self.environment = environment
    GHeap.track(self)
  }
  func start(_ args: [GValue]) -> GFrame {
    let frame = maker!((receiver.map { [GCopy($0)] } ?? []) + args, environment)
    let roots = frame.roots
    frame.roots = { roots() + [.function(self)] }
    return frame
  }
  static func start(_ value: GValue, _ args: [GValue]) throws -> GFrame {
    guard case .function(let fn) = value else {
      throw GPanic.runtime("invalid memory address or nil pointer dereference")
    }
    return fn.start(args)
  }
  static func bound(
    _ id: Int, _ maker: @escaping ([GValue], [GCell]) -> GFrame, _ receiver: GValue,
    native: GAsyncBody? = nil
  ) -> GFunction {
    let f = GFunction(id, maker, native: native)
    f.receiver = receiver
    return f
  }
  /// call runs a function value on a task of the native pause model.
  static func call(_ task: GTask, _ value: GValue, _ args: [GValue]) async throws -> [GValue] {
    guard case .function(let fn) = value else {
      throw GPanic.runtime("invalid memory address or nil pointer dereference")
    }
    if let native = fn.native {
      return try await native(task, (fn.receiver.map { [GCopy($0)] } ?? []) + args, fn.environment)
    }
    return try await task.call(fn.start(args))
  }
  func children() -> [GValue] {
    environment.map { .pointer($0) } + (receiver.map { [$0] } ?? []) + retained
  }
  func releaseEdges() {
    environment = []
    receiver = nil
    maker = nil
    retained = []
  }
}
final class GInterface: GManaged {
  let type: Int
  var value: GValue
  init(_ type: Int, _ value: GValue) {
    self.type = type
    self.value = value
    GHeap.track(self)
  }
  private static func nativeHasMethod(_ value: GValue, _ method: String) -> Bool {
    guard case .error(let error) = value else { return false }
    if method == "Error" { return true }
    if method == "RuntimeError" {
      return error.type == "runtime.errorString" || error.type == "runtime.plainError"
        || error.type == "runtime.boundsError" || error.type == "*runtime.TypeAssertionError"
        || error.type == "*runtime.PanicNilError"
    }
    if method == "Timeout" || method == "Temporary" {
      return error.type == "context.deadlineExceededError"
    }
    return false
  }
  static func start(_ v: GValue, _ method: String, _ args: [GValue]) throws -> GFrame {
    guard case .interface(let box) = v else {
      throw GPanic.runtime("invalid memory address or nil pointer dereference")
    }
    if box.type < 0 {
      if method == "Error", case .error(let error) = box.value {
        return GFrame.sync { [.string(error.bytes)] }
      }
      if method == "RuntimeError", nativeHasMethod(box.value, method) {
        return GFrame.sync { [] }
      }
      if method == "Timeout" || method == "Temporary", nativeHasMethod(box.value, method) {
        return GFrame.sync { [.bool(true)] }
      }
      throw GFault("missing method " + method)
    }
    guard let f = GTypes.table[box.type].methods[method] else {
      throw GFault("missing method " + method)
    }
    return f.start([GCopy(box.value)] + args)
  }
  /// call runs a method on a task of the native pause model.
  static func call(_ task: GTask, _ v: GValue, _ method: String, _ args: [GValue]) async throws
    -> [GValue]
  {
    if case .interface(let box) = v, box.type >= 0, let f = GTypes.table[box.type].methods[method] {
      return try await GFunction.call(task, .function(f), [GCopy(box.value)] + args)
    }
    return try await task.call(try start(v, method, args))
  }
  static func bound(_ v: GValue, _ method: String) throws -> GValue {
    guard case .interface(let box) = v else {
      throw GPanic.runtime("invalid memory address or nil pointer dereference")
    }
    let fid = box.type >= 0 ? GTypes.table[box.type].methods[method]?.id ?? -1 : -1
    let fn = GFunction(
      fid,
      { args, _ in
        do { return try start(v, method, args) } catch { return GFrame.failure(error) }
      })
    fn.retained = [v]
    return .function(fn)
  }
  static func assertType(_ v: GValue, _ type: Int, _ source: Int, _ commaOK: Bool) throws -> (
    GValue, Bool
  ) {
    let target = GTypes.table[type]
    var success = false
    var missing: String?
    if case .interface(let box) = v {
      if target.kind == "interface" {
        if box.type < 0 {
          missing = target.requiredMethods.first {
            !target.nativeMethods.contains($0) || !nativeHasMethod(box.value, $0)
          }
          success = missing == nil
        } else {
          let concrete = GTypes.table[box.type]
          missing = concrete.missingInterfaceMethods[type]
          success = target.requiredMethods.isEmpty || concrete.implementedInterfaces.contains(type)
        }
      } else {
        success = box.type == type
      }
      if success { return (target.kind == "interface" ? v : GCopy(box.value), true) }
    }
    if commaOK { return (GTypes.zero(type), false) }
    let message: String
    if case .interface(let box) = v {
      let name: String
      if box.type >= 0 {
        name = GTypes.table[box.type].name
      } else if case .error(let error) = box.value {
        name = error.type
      } else {
        throw GFault("invalid native interface")
      }
      message =
        missing.map {
          "interface conversion: " + name + " is not " + target.name + ": missing method "
            + $0.components(separatedBy: "#").first!.components(separatedBy: ".").last!
        } ?? "interface conversion: " + GTypes.table[source].name + " is " + name + ", not "
        + target.name
    } else {
      message = "interface conversion: " + GTypes.table[source].name + " is nil, not " + target.name
    }
    throw GPanic(
      .interface(GInterface(-1, .error(GError(message, "*runtime.TypeAssertionError")))))
  }
  func children() -> [GValue] { [value] }
  func releaseEdges() { value = .nilValue }
}

enum GAction {
  case call(GFrame)
  case complete([GValue])
  case resume([GValue])
  case yield, park
  /// suspend: a native task body took the turn; the task's state is already set.
  case suspend
}
final class GFrame {
  let id: Int
  var pc = 0
  var step: ((GFrame, GTask) throws -> GAction)!
  var results: (() -> [GValue]) = { [] }
  var roots: (() -> [GValue]) = { [] }
  var dynamicResults = false
  var defers: [GFrame] = []
  var finishing = false
  var panic: GPanic?
  var returned: [GValue] = []
  var isDeferred = false
  var savedPanic: GPanic?
  var savedTarget: Int?
  var native: GAsyncFrame?
  init(_ id: Int) { self.id = id }
  static func sync(_ call: @escaping () throws -> [GValue]) -> GFrame {
    let f = GFrame(-1)
    f.step = { _, _ in .complete(try call()) }
    return f
  }
  /// plain adapts a function that cannot pause to the frame calling convention.
  static func plain(_ id: Int, _ body: @escaping (GTask) throws -> [GValue]) -> GFrame {
    let f = GFrame(id)
    f.step = { _, task in .complete(try body(task)) }
    return f
  }
  /// native runs the async body of a function of the native pause model as a frame.
  static func native(_ id: Int, _ args: [GValue], _ env: [GCell], _ body: @escaping GAsyncBody)
    -> GFrame
  {
    let f = GFrame(id)
    let n = GAsyncFrame { task in try await body(task, args, env) }
    f.native = n
    f.roots = { args + env.map { .pointer($0) } }
    f.step = { frame, task in
      if n.finished {
        if let failure = n.failure { throw failure }
        return .complete(n.results)
      }
      if !n.started {
        try task.owner.start(task, n)
        n.started = true
        task.native = frame
      } else if let c = task.cont {
        task.cont = nil
        c.resume()
      }
      task.owner.away = true
      return .suspend
    }
    return f
  }
  static func failure(_ error: Error) -> GFrame {
    let f = GFrame(-1)
    f.step = { _, _ in throw error }
    return f
  }
}
final class GTask {
  let id: Int
  unowned let owner: GOwner
  var stack: [GFrame]
  var rv: [GValue] = []
  var done = false
  var blocked = false
  var queued = false
  var currentPanic: GPanic?
  var deferTarget: Int?
  var resumePanic: GPanic?
  var cleanup: (() -> Void)?
  /// native is the frame whose async body runs this task; cont resumes the
  /// body where it waits for the scheduler to choose the task again.
  var native: GFrame?
  var cont: UnsafeContinuation<Void, Error>?
  init(_ id: Int, _ owner: GOwner, _ frame: GFrame) {
    self.id = id
    self.owner = owner
    stack = [frame]
  }
  func recover(_ id: Int) -> GValue {
    guard let p = currentPanic, !p.recovered, deferTarget == id else { return .nilValue }
    p.recovered = true
    return p.value
  }
}
final class GTimer {
  let at: Int64
  let sequence: Int
  let body: () -> Void
  let retained: [GValue]
  var active = true
  init(_ at: Int64, _ seq: Int, _ retained: [GValue], _ body: @escaping () -> Void) {
    self.at = at
    sequence = seq
    self.retained = retained
    self.body = body
  }
}
final class GMailbox {
  let condition = NSCondition()
  var completions: [() -> Void] = []
  func publish(_ completion: @escaping () -> Void) {
    condition.lock()
    completions.append(completion)
    condition.signal()
    condition.unlock()
  }
  func drain() {
    condition.lock()
    let ready = completions
    completions = []
    condition.unlock()
    for completion in ready { completion() }
  }
}
final class GOwner {
  let host: Bool
  let thread = pthread_self()
  let epoch = DispatchTime.now().uptimeNanoseconds
  let mailbox = GMailbox()
  var tasks: [GTask] = []
  var queue: [GTask] = []
  var timers: [GTimer] = []
  var operations: [Int: GHostOperation] = [:]
  var operationID = 0
  var clock: Int64 = 0
  var seq = 0
  var rng: UInt32
  var active = true
  var main: GTask?
  var callbacks: [String: CallbackProvider] = [:]
  var keys: [GWeakCryptoKey] = []
  /// Library calls receive the keys still open at shutdown instead of closing
  /// them, so an instance can keep them; abandoned counts unfinished goroutines.
  var retainKeys: ((GCryptoKey) -> Void)?
  var abandoned = 0
  /// GOALCHEMY_GC_THRESHOLD overrides the allocations between collections
  /// (1 collects at every safepoint).
  let collectionThreshold: Int?
  var nextCollection: Int
  /// syncDepth counts synchronous calls in progress; their callers' locals
  /// are not frame roots, so collection waits until they return.
  var syncDepth = 0
  var harness = false
  /// away is set when a native task body took the turn from the loop that
  /// dispatched it; back is set when the turn returns to the driver.
  var away = false
  var back: GHandoff?
  var executor: AnyObject?
  static var current: GOwner?
  init(host: Bool) {
    self.host = host
    let seed = UInt32(ProcessInfo.processInfo.environment["GOALCHEMY_SEED"] ?? "1") ?? 1
    rng = seed == 0 ? 1 : seed
    collectionThreshold = ProcessInfo.processInfo.environment["GOALCHEMY_GC_THRESHOLD"].flatMap {
      Int($0)
    }
    nextCollection = collectionThreshold ?? 4096
  }
  func check() throws {
    if pthread_equal(thread, pthread_self()) == 0 {
      throw GFault("source accessed from native callback")
    }
  }
  func now() -> Int64 {
    if host {
      clock = max(
        clock, Int64(min(UInt64(Int64.max), DispatchTime.now().uptimeNanoseconds - epoch)))
    }
    return clock
  }
  func choose(_ n: Int) -> Int {
    rng ^= rng << 13
    rng ^= rng >> 17
    rng ^= rng << 5
    return Int(rng % UInt32(n))
  }
  func ready(_ task: GTask, _ values: [GValue]? = nil) {
    guard active, !task.done, task.owner === self else { return }
    if let v = values { task.rv = v }
    task.blocked = false
    if !task.queued {
      task.queued = true
      queue.append(task)
    }
  }
  @discardableResult func spawn(_ frame: GFrame) -> GTask {
    let t = GTask(tasks.count, self, frame)
    tasks.append(t)
    ready(t)
    return t
  }
  @discardableResult func timer(
    _ delay: Int64, retained: [GValue] = [], _ body: @escaping () -> Void
  ) -> GTimer {
    seq += 1
    let sum = now().addingReportingOverflow(max(0, delay))
    let t = GTimer(sum.overflow ? Int64.max : sum.partialValue, seq, retained, body)
    timers.append(t)
    return t
  }
  func fireTimers(_ advance: Bool) {
    if timers.isEmpty { return }
    let active = timers.filter { $0.active }
    if advance, let next = active.map({ $0.at }).min() { clock = max(clock, next) }
    let due = active.filter { $0.at <= now() }.sorted {
      $0.at == $1.at ? $0.sequence < $1.sequence : $0.at < $1.at
    }
    for timer in due where timer.active {
      timer.active = false
      timer.body()
    }
    timers.removeAll { !$0.active }
  }
  func run(_ frame: GFrame) throws -> [GValue] {
    try check()
    if !active { throw GFault("retired owner") }
    let previous = GOwner.current
    GOwner.current = self
    defer {
      GOwner.current = previous
      shutdown()
    }
    let root = spawn(frame)
    main = root
    while !root.done {
      guard let task = poll() else {
        try idle()
        continue
      }
      try drive(task)
      if away { try turnBack() }
    }
    GTransferKeys(root.rv)
    return root.rv
  }
  /// poll returns the next runnable task after due timers and host
  /// completions, or nil.
  func poll() -> GTask? {
    while true {
      fireTimers(false)
      mailbox.drain()
      if queue.isEmpty { return nil }
      let task = queue.removeFirst()
      task.queued = false
      if !task.done { return task }
    }
  }
  /// idle makes progress when no task is runnable: it fires the next timers,
  /// waits for host completions, or reports a deadlock.
  func idle() throws {
    if !host && !timers.isEmpty && operations.isEmpty {
      fireTimers(true)
      return
    }
    if !operations.isEmpty || (host && !timers.isEmpty) {
      mailbox.condition.lock()
      if mailbox.completions.isEmpty {
        if let next = timers.filter({ $0.active }).map({ $0.at }).min() {
          _ = mailbox.condition.wait(
            until: Date(timeIntervalSinceNow: Double(max(0, next - now())) / 1e9))
        } else {
          mailbox.condition.wait()
        }
      }
      mailbox.condition.unlock()
      return
    }
    throw GFatal("all goroutines are asleep - deadlock!")
  }
  /// callSync runs a frame that cannot pause to completion on the current
  /// task and returns its results; an unrecovered panic is thrown.
  func callSync(_ task: GTask, _ frame: GFrame) throws -> [GValue] {
    let base = task.stack.count
    task.stack.append(frame)
    syncDepth += 1
    defer { syncDepth -= 1 }
    try drive(task, base: base)
    return task.rv
  }
  /// drive steps a task's frames above base until it blocks or they finish.
  /// The scheduler drives from base 0; inline drives the frames a native
  /// task body calls, returning when one pauses or is a native frame, which
  /// the body awaits itself.
  func drive(_ task: GTask, base: Int = 0, inline: Bool = false) throws {
    while !task.done && !task.blocked && task.stack.count > base {
      if base == 0, task.cont != nil, let n = task.native {
        _ = try n.step(n, task)
        return
      }
      guard let frame = task.stack.last else {
        task.done = true
        break
      }
      if base > 0, let n = frame.native, !n.started {
        if inline { return }
        throw GFault("synchronous call paused")
      }
      if let p = task.resumePanic {
        task.resumePanic = nil
        frame.panic = p
        frame.finishing = true
      }
      if frame.finishing {
        if let d = frame.defers.popLast() {
          d.isDeferred = true
          d.savedPanic = task.currentPanic
          d.savedTarget = task.deferTarget
          task.currentPanic = frame.panic
          task.deferTarget = d.id
          task.stack.append(d)
          continue
        }
        task.stack.removeLast()
        let panicking = frame.panic
        if frame.isDeferred {
          task.currentPanic = frame.savedPanic
          task.deferTarget = frame.savedTarget
          if let parent = task.stack.last {
            if let p = panicking {
              if p !== parent.panic, p.previous == nil { p.previous = parent.panic }
              parent.panic = p
            } else if parent.panic?.recovered == true {
              parent.panic = nil
            }
          }
          continue
        }
        if let p = panicking, !p.recovered {
          if task.stack.count == base && base > 0 { throw p }
          if let parent = task.stack.last {
            parent.panic = p
            parent.finishing = true
            continue
          }
          throw p
        }
        task.rv = frame.results()
        if task.stack.isEmpty && base == 0 {
          task.done = true
          break
        }
        continue
      }
      let action: GAction
      do { action = try frame.step(frame, task) } catch let p as GPanic {
        frame.panic = p
        frame.finishing = true
        continue
      }
      switch action {
      case .call(let child): task.stack.append(child)
      case .complete(let values):
        frame.returned = values
        if !frame.dynamicResults { frame.results = { values } }
        frame.finishing = true
      case .resume(let values): task.rv = values
      case .yield:
        if base > 0 && !inline { throw GFault("synchronous call paused") }
        ready(task)
        return
      case .park:
        if base > 0 && !inline { throw GFault("synchronous call paused") }
        task.blocked = true
        return
      case .suspend: return
      }
    }
  }
  func shutdown() {
    if !active { return }
    active = false
    abandoned = tasks.filter { !$0.done && $0 !== main }.count
    unwindNative()
    for task in tasks {
      task.cleanup?()
      task.cleanup = nil
      task.stack = []
      task.done = true
    }
    for operation in operations.values { operation.cancel() }
    while !operations.isEmpty {
      mailbox.drain()
      if !operations.isEmpty {
        mailbox.condition.lock()
        if mailbox.completions.isEmpty {
          _ = mailbox.condition.wait(until: Date(timeIntervalSinceNow: 0.1))
        }
        mailbox.condition.unlock()
      }
    }
    queue = []
    timers = []
    tasks = []
    for weak in keys {
      if let key = weak.value, !key.escaped {
        if let retain = retainKeys {
          key.escaped = true
          retain(key)
        } else {
          key.close()
        }
      }
    }
    keys = []
  }
}

// An operation is retired only after the native worker confirms cleanup.
final class GHostOperation {
  let id: Int
  let task: GTask
  var cancelNative: (() -> Void)?
  var canceled = false
  var timedOut = false
  var retained: [GValue] = []
  init(_ id: Int, _ task: GTask) {
    self.id = id
    self.task = task
  }
  func cancel() {
    if !canceled {
      canceled = true
      cancelNative?()
    }
  }
}

func GTransferKeys(_ values: [GValue]) {
  var seen = Set<ObjectIdentifier>()
  var todo = values
  while let v = todo.popLast() {
    switch v {
    case .pointer(let c): if seen.insert(ObjectIdentifier(c)).inserted { todo.append(c.value) }
    case .opaque(let key as GCryptoKey): key.escaped = true
    case .aggregate(let a): if seen.insert(ObjectIdentifier(a)).inserted { todo += a.children() }
    case .slice(let s):
      if let b = s.storage, seen.insert(ObjectIdentifier(b)).inserted { todo += b.children() }
    default: break
    }
  }
}
extension GOwner {
  func safepoint() {
    if syncDepth > 0 || GHeap.allocations.count < nextCollection { return }
    var roots: [GValue] = []
    for task in tasks where !task.done {
      roots += task.rv
      for frame in task.stack {
        roots += frame.roots()
        for d in frame.defers { roots += d.roots() }
      }
    }
    roots += operations.values.flatMap { $0.retained }
    roots += timers.filter { $0.active }.flatMap { $0.retained }
    roots += GLibrary.retained.values.flatMap { $0 }
    roots += GTypes.table.flatMap { $0.methods.values.map { GValue.function($0) } }
    roots += [GNative.canceledError, GNative.deadlineError]
    for task in tasks {
      if let p = task.currentPanic { roots.append(p.value) }
      for frame in task.stack { if let p = frame.panic { roots.append(p.value) } }
    }
    func panicRoots(_ p: GPanic?) -> [GValue] {
      guard let p = p else { return [] }
      return [p.value] + panicRoots(p.previous)
    }
    for task in tasks {
      roots += panicRoots(task.currentPanic)
      for frame in task.stack {
        roots += panicRoots(frame.panic) + panicRoots(frame.savedPanic)
        roots += frame.returned
      }
    }
    if let background = GNative.background { roots.append(.opaque(background)) }
    GHeap.collect(roots)
    keys.removeAll { $0.value == nil }
    nextCollection = collectionThreshold ?? max(4096, GHeap.allocations.count * 2)
  }
}
