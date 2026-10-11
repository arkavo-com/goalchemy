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
    if Thread.current.threadDictionary["goalchemy.stream"] != nil {
      throw GoalchemyFailure("reentrant", "host stream called back into the library")
    }
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
    if Thread.current.threadDictionary["goalchemy.stream"] != nil {
      throw GoalchemyFailure("reentrant", "host stream called back into the library")
    }
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
  var streams: [String: CallbackProvider] = [:]
  var nextStream: Int64 = 0
  var objects: [ObjectIdentifier: (GHandle, GWeakHandle)] = [:]
  func retire() {
    if retired { return }
    retired = true
    GLibrary.retained[id] = nil
    streams = [:]
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
    _ closing: GHandle?, _ streams: [CallbackProvider?], _ ids: GStreamIDs,
    _ initialize: @escaping () -> GFrame,
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
      for (i, stream) in streams.enumerated() {
        guard let stream = stream else { continue }
        inst.nextStream += 1
        ids.values[i] = inst.nextStream
        inst.streams["goalchemy.stream.\(inst.nextStream)"] = stream
      }
      owner.callbacks = options.callbacks.merging(inst.streams) { _, stream in stream }
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
    try submitHandles([], CallOptions(), state, [handle], handle, [], GStreamIDs(0), initialize, run, decode).wait()
  }
}

/// Stream ids assigned when a call registers its host streams.
final class GStreamIDs {
  var values: [Int64]
  init(_ count: Int) { values = Array(repeating: 0, count: count) }
}
/// A host stream passed for a std/io parameter. Set the operations the
/// parameter needs; read returns an empty array at end of stream.
public struct GoalchemyStream {
  public var read: ((Int) throws -> [UInt8])?
  public var write: (([UInt8]) throws -> Int)?
  public var seek: ((Int64, Int) throws -> Int64)?
  public var close: (() throws -> Void)?
  public init(
    read: ((Int) throws -> [UInt8])? = nil, write: (([UInt8]) throws -> Int)? = nil,
    seek: ((Int64, Int) throws -> Int64)? = nil, close: (() throws -> Void)? = nil
  ) {
    self.read = read
    self.write = write
    self.seek = seek
    self.close = close
  }
  /// A seekable reader over a copy of bytes.
  public init(bytes: [UInt8]) {
    final class Cursor { var position = 0 }
    let cursor = Cursor()
    self.init(
      read: { n in
        let end = min(bytes.count, cursor.position + n)
        defer { cursor.position = max(cursor.position, end) }
        return cursor.position >= end ? [] : Array(bytes[cursor.position..<end])
      },
      seek: { offset, whence in
        let base = whence == 0 ? 0 : whence == 1 ? Int64(cursor.position) : Int64(bytes.count)
        if base + offset < 0 { throw GoalchemyFailure("source", "seek: negative position") }
        cursor.position = Int(base + offset)
        return base + offset
      })
  }
  /// A reader over a Foundation InputStream; close closes it.
  public init(input: InputStream) {
    if input.streamStatus == .notOpen { input.open() }
    self.init(
      read: { n in
        var buffer = [UInt8](repeating: 0, count: n)
        let k = input.read(&buffer, maxLength: n)
        if k < 0 { throw input.streamError ?? GoalchemyFailure("source", "read failed") }
        return Array(buffer[0..<k])
      }, close: { input.close() })
  }
  /// A writer over a Foundation OutputStream; close closes it.
  public init(output: OutputStream) {
    if output.streamStatus == .notOpen { output.open() }
    self.init(
      write: { data in
        var written = 0
        while written < data.count {
          let k = data[written...].withUnsafeBufferPointer { output.write($0.baseAddress!, maxLength: $0.count) }
          if k <= 0 { throw output.streamError ?? GoalchemyFailure("source", "write failed") }
          written += k
        }
        return written
      }, close: { output.close() })
  }
}
extension GLibrary {
  private static func streamInt(_ b: [UInt8], _ at: Int) -> Int64 {
    var v: UInt64 = 0
    for i in 0..<8 { v = v << 8 | UInt64(b[at + i]) }
    return Int64(bitPattern: v)
  }
  private static func streamReply(_ v: Int64) -> [UInt8] {
    var out: [UInt8] = [0]
    var x = UInt64(bitPattern: v)
    var bytes = [UInt8](repeating: 0, count: 8)
    for i in stride(from: 7, through: 0, by: -1) { bytes[i] = UInt8(x & 255); x >>= 8 }
    out += bytes
    return out
  }
  private static func serve(_ s: GoalchemyStream, _ req: [UInt8]) throws -> [UInt8] {
    struct Malformed: Error, CustomStringConvertible { var description: String { "hostio: malformed stream request" } }
    switch req.first {
    case UInt8(ascii: "r") where req.count == 9:
      let n = Int(streamInt(req, 1))
      guard n >= 0, n < 128 << 10, let read = s.read else { throw Malformed() }
      let data = try read(n)
      if data.count > n { throw Malformed() }
      return [n > 0 && data.isEmpty ? 1 : 0] + data
    case UInt8(ascii: "w"):
      guard let write = s.write else { throw Malformed() }
      return streamReply(Int64(try write(Array(req[1...]))))
    case UInt8(ascii: "s") where req.count == 10:
      guard let seek = s.seek else { throw Malformed() }
      return streamReply(try seek(streamInt(req, 1), Int(req[9])))
    case UInt8(ascii: "c"):
      try s.close?()
      return [0]
    default: throw Malformed()
    }
  }
  /// Adapts a host stream to the provider serving one std/io parameter;
  /// reads and writes copy their buffers and end of stream becomes io.EOF.
  static func stream(_ s: GoalchemyStream?, _ kind: String) throws -> CallbackProvider? {
    guard let s = s else { return nil }
    if (kind.contains("Read") && s.read == nil) || (kind.contains("Write") && s.write == nil)
      || (kind.contains("Seek") && s.seek == nil)
    {
      throw GoalchemyFailure("invalid_argument", "stream does not support \(kind)")
    }
    return { request, settle in
      Thread.current.threadDictionary["goalchemy.stream"] = true
      defer { Thread.current.threadDictionary.removeObject(forKey: "goalchemy.stream") }
      do {
        settle(.success(try serve(s, request.bytes)))
      } catch {
        let message: String
        if let f = error as? GoalchemyFailure { message = f.message } else { message = String(describing: error) }
        settle(.success([2] + Array(message.utf8)))
      }
      return nil
    }
  }
}
extension GLibrary {
  /// Runs each stream adapter (nil keeps its zero value), then the call
  /// built from the adapted values; completes with the call's results.
  static func streamFrame(_ adapters: [(() -> GFrame)?], _ zeros: [GValue], _ call: @escaping ([GValue]) -> GFrame) -> GFrame {
    var values = zeros
    var next = 0
    var waiting = -1
    let frame = GFrame(-1)
    frame.roots = { values }
    frame.step = { frame, task in
      if waiting >= 0 {
        values[waiting] = task.rv.first ?? zeros[waiting]
        waiting = -1
      }
      while next < adapters.count {
        let i = next
        next += 1
        if let adapt = adapters[i] {
          waiting = i
          return .call(adapt())
        }
      }
      if frame.pc == 0 {
        frame.pc = 1
        return .call(call(values))
      }
      return .complete(task.rv)
    }
    return frame
  }
}

/// Source std/io operations behind a host stream view; each one blocks on a
/// library call on the handle. read returns the data and whether the source
/// reached io.EOF.
struct GStreamOps {
  var read: ((GHandle, Int64) throws -> ([UInt8], Bool))?
  var write: ((GHandle, [UInt8]) throws -> Int64)?
  var seek: ((GHandle, Int64, Int64) throws -> Int64)?
  var close: (GHandle) throws -> Void
}
/// The host view of a source std/io reader or writer. Operations block until
/// their library call completes; an operation the source lacks throws kind
/// "unsupported". close() runs the source Close, if any; deinit releases the
/// handle without it. Iterate it with for try await to read Data chunks.
public final class GoalchemySourceStream: AsyncSequence {
  public typealias Element = Data
  let handle: GHandle
  let ops: GStreamOps
  let owned: Bool
  init(_ handle: GHandle, _ ops: GStreamOps, owned: Bool = true) {
    self.handle = handle
    self.ops = ops
    self.owned = owned
  }
  deinit { if owned { GLibrary.finalize(handle) } }
  public var canRead: Bool { ops.read != nil }
  public var canWrite: Bool { ops.write != nil }
  public var canSeek: Bool { ops.seek != nil }
  /// Reads up to maxLength bytes; nil at end of stream.
  public func read(_ maxLength: Int = 64 << 10) throws -> [UInt8]? {
    guard let read = ops.read else { throw GoalchemyFailure("unsupported", "stream cannot read") }
    if maxLength <= 0 { return [] }
    let (data, eof) = try read(handle, Int64(Swift.min(maxLength, 1 << 20)))
    return data.isEmpty && eof ? nil : data
  }
  /// Reads into buffer; returns the count, or 0 at end of stream.
  public func read(into buffer: UnsafeMutableBufferPointer<UInt8>) throws -> Int {
    guard let data = try read(buffer.count) else { return 0 }
    _ = buffer.initialize(from: data)
    return data.count
  }
  /// Writes all of data.
  @discardableResult public func write(_ data: [UInt8]) throws -> Int {
    guard let write = ops.write else { throw GoalchemyFailure("unsupported", "stream cannot write") }
    var done = 0
    repeat {
      let end = Swift.min(data.count, done + (1 << 20))
      done += Int(try write(handle, Array(data[done..<end])))
    } while done < data.count
    return done
  }
  /// Seeks; whence is 0 (start), 1 (current) or 2 (end).
  public func seek(_ offset: Int64, _ whence: Int = 0) throws -> Int64 {
    guard let seek = ops.seek else { throw GoalchemyFailure("unsupported", "stream cannot seek") }
    guard (0...2).contains(whence) else { throw GoalchemyFailure("invalid_argument", "whence must be 0, 1 or 2") }
    return try seek(handle, offset, Int64(whence))
  }
  /// Runs the source Close, if any, and releases the handle.
  public func close() throws { try ops.close(handle) }
  public struct AsyncIterator: AsyncIteratorProtocol {
    let stream: GoalchemySourceStream
    public mutating func next() async throws -> Data? { try stream.read().map { Data($0) } }
  }
  public func makeAsyncIterator() -> AsyncIterator { AsyncIterator(stream: self) }
}
