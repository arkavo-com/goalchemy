// SPDX-License-Identifier: Apache-2.0
import Foundation

/// Go strings are arbitrary bytes. String literals use UTF-8; byte construction
/// preserves invalid UTF-8 and NUL without normalization.
public struct GoString: ExpressibleByStringLiteral, Equatable, CustomStringConvertible {
  public var bytes: [UInt8]
  public init(_ string: String) { bytes = Array(string.utf8) }
  public init(bytes: [UInt8]) { self.bytes = bytes }
  public init(stringLiteral value: String) { self.init(value) }
  public var description: String { String(decoding: bytes, as: UTF8.self) }
}
public struct GoalchemyFailure: Error, CustomStringConvertible {
  public let kind: String
  public let message: String
  public let fields: [String: Any]
  public init(_ kind: String, _ message: String, _ fields: [String: Any] = [:]) {
    self.kind = kind
    self.message = message
    self.fields = fields
  }
  public var description: String { message }
}
public final class CancellationToken {
  private let lock = NSLock()
  private var canceled = false
  private var hook: (() -> Void)?
  public init() {}
  public var isCanceled: Bool {
    lock.lock()
    defer { lock.unlock() }
    return canceled
  }
  public func cancel() {
    lock.lock()
    canceled = true
    let h = hook
    lock.unlock()
    h?()
  }
  func install(_ fn: (() -> Void)?) {
    lock.lock()
    hook = fn
    let already = canceled
    lock.unlock()
    if already { fn?() }
  }
}
public struct CallbackRequest {
  public let bytes: [UInt8]
  public let cancellation: CancellationToken
}
public typealias CallbackProvider = (CallbackRequest, @escaping (Result<[UInt8], Error>) -> Void) ->
  (() -> Void)?
public struct CallOptions {
  public var cancellation: CancellationToken
  public var timeoutNanoseconds: Int64?
  public var callbacks: [String: CallbackProvider]
  public init(
    cancellation: CancellationToken = CancellationToken(), timeoutNanoseconds: Int64? = nil,
    callbacks: [String: CallbackProvider] = [:]
  ) {
    self.cancellation = cancellation
    self.timeoutNanoseconds = timeoutNanoseconds
    self.callbacks = callbacks
  }
}
public final class Operation<T> {
  public let cancellation: CancellationToken
  private let condition = NSCondition()
  private var body: (() throws -> T)?
  private var running = false
  private var result: Result<T, Error>?
  init(_ cancellation: CancellationToken, _ body: @escaping () throws -> T) {
    self.cancellation = cancellation
    self.body = body
  }
  public func cancel() { cancellation.cancel() }
  public func wait() throws -> T {
    if Thread.current.threadDictionary["goalchemy.provider"] != nil {
      throw GoalchemyFailure("host", "reentrant source call from callback provider")
    }
    condition.lock()
    while running && result == nil { condition.wait() }
    if let result = result {
      condition.unlock()
      return try result.get()
    }
    running = true
    let call = body!
    body = nil
    condition.unlock()
    let result = Result { try call() }
    condition.lock()
    self.result = result
    running = false
    condition.broadcast()
    condition.unlock()
    return try result.get()
  }
  public func value() async throws -> T {
    try await withCheckedThrowingContinuation { continuation in
      DispatchQueue.global().async { continuation.resume(with: Result { try self.wait() }) }
    }
  }
}
enum GLibrary {
  static func provider<T>(_ body: () -> T) -> T {
    let thread = Thread.current
    let prior = thread.threadDictionary["goalchemy.provider"]
    thread.threadDictionary["goalchemy.provider"] = true
    defer {
      if let prior = prior {
        thread.threadDictionary["goalchemy.provider"] = prior
      } else {
        thread.threadDictionary.removeObject(forKey: "goalchemy.provider")
      }
    }
    return body()
  }
  static let lock = NSRecursiveLock()
  static var retained: [UUID: [GValue]] = [:]
  static func prepare(_ convert: () throws -> [GValue]) throws -> [GValue] {
    if Thread.current.threadDictionary["goalchemy.provider"] != nil {
      throw GoalchemyFailure("host", "reentrant source call from callback provider")
    }
    lock.lock()
    defer { lock.unlock() }
    return try convert()
  }
  static func submit<T>(
    _ args: [GValue], _ options: CallOptions, _ reset: @escaping () -> Void,
    _ initialize: @escaping () -> GFrame, _ start: @escaping ([GValue], GValue) -> GFrame,
    _ decode: @escaping ([GValue]) throws -> T
  ) -> Operation<T> {
    lock.lock()
    let id = UUID()
    retained[id] = args
    lock.unlock()
    let held = GLibraryRetention(id)
    return Operation(options.cancellation) {
      _ = held
      lock.lock()
      defer {
        retained[id] = nil
        GHeap.collect(retained.values.flatMap { $0 })
        lock.unlock()
      }
      reset()
      let owner = GOwner(host: true)
      owner.callbacks = options.callbacks
      let elem =
        GTypes.table.firstIndex { $0.kind == "struct" && $0.fields.isEmpty }
        ?? GNative.addPrimitive("struct {}", "struct", 0, false)
      let context = GContext(elem)
      context.owner = owner
      options.cancellation.install { owner.mailbox.publish { context.cancel() } }
      defer { options.cancellation.install(nil) }
      if options.cancellation.isCanceled { context.cancel() }
      if let delay = options.timeoutNanoseconds {
        if delay <= 0 {
          context.cancel(true)
        } else {
          context.timer = owner.timer(delay, retained: [.opaque(context)]) { context.cancel(true) }
        }
      }
      let frame = GFrame(-1)
      frame.roots = { args + [.opaque(context)] }
      frame.step = { frame, task in
        if frame.pc == 0 {
          frame.pc = 1
          return .call(initialize())
        }
        if frame.pc == 1 {
          frame.pc = 2
          return .call(start(args, .opaque(context)))
        }
        return .complete(task.rv)
      }
      do { return try decode(owner.run(frame)) } catch let p as GPanic {
        throw GoalchemyFailure("panic", GPanicText(p))
      } catch let e as GFault { throw GoalchemyFailure("host", e.message) } catch let e as GFatal {
        throw GoalchemyFailure("runtime", e.message)
      }
    }
  }
}
final class GLibraryRetention {
  let id: UUID
  init(_ id: UUID) { self.id = id }
  deinit {
    GLibrary.lock.lock()
    GLibrary.retained[id] = nil
    GLibrary.lock.unlock()
  }
}

/// Saves, restores and zeroes generated source globals.
struct GLibraryState {
  let reset: () -> Void
  let save: () -> [GValue]
  let load: ([GValue]) -> Void
}
/// Persistent source state behind a family of handles; guarded by GLibrary.lock.
final class GInstance {
  let id = UUID()
  var live = 0
  var poisoned = false
  var retired = false
  var keys: [GCryptoKey] = []
  var objects: [ObjectIdentifier: (GHandle, GWeakHandle)] = [:]
  func retire() {
    if retired { return }
    retired = true
    GLibrary.retained[id] = nil
    for key in keys { key.close() }
    keys = []
    for (handle, _) in objects.values { handle.drop() }
    objects = [:]
  }
}
final class GWeakHandle {
  weak var value: AnyObject?
  init(_ value: AnyObject) { self.value = value }
}
/// The host's reference to one source object of an instance.
public final class GHandle {
  let instance: GInstance
  let value: GValue
  let name: String
  private var root: UUID?
  private let state = NSLock()
  private var isClosed = false
  var released = false
  var active: CancellationToken?
  init(_ instance: GInstance, _ value: GValue, _ name: String) {
    self.instance = instance
    self.value = value
    self.name = name
    root = GHeap.retainExternal(value)
  }
  var closed: Bool {
    state.lock()
    defer { state.unlock() }
    return isClosed
  }
  /// Marks the handle closed; returns false when it already was.
  func markClosed() -> Bool {
    state.lock()
    defer { state.unlock() }
    if isClosed { return false }
    isClosed = true
    return true
  }
  func drop() {
    if let r = root { GHeap.releaseExternal(r) }
    root = nil
  }
  /// Requires GLibrary.lock.
  func release() {
    if released { return }
    released = true
    if let key = GLibrary.identity(value), let entry = instance.objects[key], entry.0 === self {
      instance.objects[key] = nil
    }
    drop()
    instance.live -= 1
    if instance.live == 0 { instance.retire() }
  }
}
/// Boundary warnings such as leaked handles and abandoned goroutines.
public func setGoalchemyWarningHandler(_ handler: ((String) -> Void)?) {
  GLibrary.lock.lock()
  GLibrary.warn = handler
  GLibrary.lock.unlock()
}
extension GLibrary {
  static var warn: ((String) -> Void)?
  static func warning(_ message: String) {
    if let w = warn { w(message) } else { FileHandle.standardError.write(Data(("goalchemy: warning: " + message + "\n").utf8)) }
  }
  static func identity(_ value: GValue) -> ObjectIdentifier? {
    if case .pointer(let cell) = value { return ObjectIdentifier(cell) }
    return nil
  }
  /// Returns the host wrapper for a source pointer, reusing the live wrapper of
  /// the same pointer so host identity follows source identity. Requires lock.
  static func wrap<W: AnyObject>(_ instance: GInstance, _ value: GValue, _ name: String, _ make: (GHandle) -> W) -> W? {
    guard let key = identity(value) else { return nil }
    if let (handle, weak) = instance.objects[key], !handle.closed, let w = weak.value as? W { return w }
    let handle = GHandle(instance, value, name)
    let w = make(handle)
    instance.objects[key] = (handle, GWeakHandle(w))
    instance.live += 1
    return w
  }
  /// Releases a handle whose wrapper was deinitialized without close.
  static func finalize(_ handle: GHandle) {
    if !handle.markClosed() { return }
    lock.lock()
    defer { lock.unlock() }
    if handle.released { return }
    warning(handle.name + " handle was not closed; releasing it without running Close")
    handle.release()
  }
  /// Runs one call. handles lists handle arguments, receiver first; a call with
  /// no live handle creates a fresh owner that becomes an instance when its
  /// results include handles. A closing call releases closing afterwards.
  static func submitHandles<T>(
    _ args: [GValue], _ options: CallOptions, _ state: GLibraryState, _ handles: [GHandle?],
    _ closing: GHandle?, _ initialize: @escaping () -> GFrame,
    _ start: @escaping ([GValue], GValue, [GValue]) -> GFrame,
    _ decode: @escaping ([GValue], GInstance) throws -> T
  ) -> Operation<T> {
    lock.lock()
    let id = UUID()
    retained[id] = args
    lock.unlock()
    let held = GLibraryRetention(id)
    let me = handles.compactMap { $0 }.first.flatMap { $0 === closing ? nil : $0 }
    return Operation(options.cancellation) {
      _ = held
      if let me = me, me.closed { throw GoalchemyFailure("closed", "handle is closed") }
      lock.lock()
      var instance: GInstance?
      var fresh = true
      var keep = false
      defer {
        retained[id] = nil
        if let me = me, me.active === options.cancellation { me.active = nil }
        if let c = closing { c.release() } else if keep, let i = instance, i.live == 0 { i.retire() }
        GHeap.collect(retained.values.flatMap { $0 })
        lock.unlock()
      }
      for h in handles.compactMap({ $0 }) {
        if h.released || (h !== closing && h.closed) || h.instance.retired {
          throw GoalchemyFailure("closed", "handle is closed")
        }
        if let i = instance, i !== h.instance {
          throw GoalchemyFailure("instance_mismatch", "handles of different instances")
        }
        instance = h.instance
      }
      fresh = instance == nil
      let inst = instance ?? GInstance()
      instance = inst
      keep = !fresh
      if inst.poisoned {
        if closing != nil { return try decode([.nilValue], inst) }
        throw GoalchemyFailure("poisoned", "instance is poisoned")
      }
      if let me = me {
        me.active = options.cancellation
        if me.closed { options.cancellation.cancel() }
      }
      if fresh { state.reset() } else { state.load(retained[inst.id] ?? []) }
      let owner = GOwner(host: true)
      owner.callbacks = options.callbacks
      var pendingKeys: [GCryptoKey] = []
      owner.retainKeys = { pendingKeys.append($0) }
      let elem =
        GTypes.table.firstIndex { $0.kind == "struct" && $0.fields.isEmpty }
        ?? GNative.addPrimitive("struct {}", "struct", 0, false)
      let context = GContext(elem)
      context.owner = owner
      options.cancellation.install { owner.mailbox.publish { context.cancel() } }
      defer { options.cancellation.install(nil) }
      if options.cancellation.isCanceled { context.cancel() }
      if let delay = options.timeoutNanoseconds {
        if delay <= 0 {
          context.cancel(true)
        } else {
          context.timer = owner.timer(delay, retained: [.opaque(context)]) { context.cancel(true) }
        }
      }
      let objects = handles.map { $0.map { $0.value } ?? .nilValue }
      let frame = GFrame(-1)
      frame.roots = { args + objects + [.opaque(context)] }
      frame.pc = fresh ? 0 : 1
      frame.step = { frame, task in
        if frame.pc == 0 {
          frame.pc = 1
          return .call(initialize())
        }
        if frame.pc == 1 {
          frame.pc = 2
          return .call(start(args, .opaque(context), objects))
        }
        return .complete(task.rv)
      }
      let finish = { (failed: Bool) in
        keep = !fresh || inst.live > 0
        if keep {
          inst.keys += pendingKeys
          if !failed {
            retained[inst.id] = state.save()
            if owner.abandoned > 0 {
              warning("\(owner.abandoned) goroutine(s) still running when a handle call returned were abandoned")
            }
          }
        } else {
          for key in pendingKeys { key.close() }
        }
      }
      do {
        let rv = try owner.run(frame)
        if !fresh && options.cancellation.isCanceled {
          finish(false)
          throw GoalchemyFailure("canceled", "call canceled")
        }
        let result = try decode(rv, inst)
        finish(false)
        return result
      } catch let p as GPanic {
        finish(true)
        if !fresh { inst.poisoned = true }
        throw GoalchemyFailure("panic", GPanicText(p))
      } catch let e as GFault {
        finish(true)
        if !fresh { inst.poisoned = true }
        throw GoalchemyFailure("host", e.message)
      } catch let e as GFatal {
        finish(true)
        if !fresh { inst.poisoned = true }
        throw GoalchemyFailure("runtime", e.message)
      } catch {
        finish(false)
        throw error
      }
    }
  }
  /// Fails calls queued on handle, cancels its active call, runs the source
  /// Close through start when the instance is healthy, then releases it.
  static func close(
    _ handle: GHandle, _ state: GLibraryState, _ initialize: @escaping () -> GFrame,
    _ start: (([GValue], GValue, [GValue]) -> GFrame)?, _ decode: @escaping ([GValue], GInstance) throws -> Void
  ) throws {
    if !handle.markClosed() { return }
    handle.active?.cancel()
    let run = start ?? { _, _, _ in GFrame.sync { [.nilValue] } }
    try submitHandles([], CallOptions(), state, [handle], handle, initialize, run, decode).wait()
  }
}
