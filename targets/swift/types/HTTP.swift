// SPDX-License-Identifier: Apache-2.0
import Foundation
import GoalchemyNative

struct GHTTPInput {
  let method: [UInt8]
  let url: [UInt8]
  let headers: [[UInt8]]
  let body: [UInt8]
  let limit: Int
  let timeout: Int64
  let tls: GHTTPTLS?
}
struct GHTTPTLS {
  var cert: [UInt8]?
  var key: [UInt8]?
  var roots: [UInt8]?
  var insecure = false
  var socket: [UInt8]?
}
struct GHTTPOutput {
  let status: Int64
  let headers: [[UInt8]]
  let body: [UInt8]
  let error: String?
}
enum GHTTP {
  static func zero(_ error: GValue) -> [GValue] {
    [.int(0), GNative.nilStrings(), GNative.nilBytes(), error]
  }
  static func validate(_ args: [GValue]) throws -> GHTTPInput {
    let method = args[1].bytes
    let url = args[2].bytes
    let headers = try GElements(args[3]).map { $0.bytes }
    let body = try GNative.buffer(args[4])
    let limit = args[5].intValue
    let timeout = args[6].intValue
    let methodText = String(decoding: method, as: UTF8.self)
    if (methodText != "GET" && methodText != "POST") || (methodText == "GET" && !body.isEmpty)
      || body.count > 64 << 20 || limit < 0 || limit > 64 << 20 || timeout < 1 || timeout > 300000
      || headers.count % 2 != 0 || url.count > 8192
      || url.contains(where: { $0 <= 32 || $0 == 127 || $0 == 92 })
    {
      throw GReject("http: invalid request or limit")
    }
    try check(url, headers)
    let tls = args.count > 7 ? try options(GElements(args[7]).map { $0.bytes }) : nil
    return GHTTPInput(
      method: method, url: url, headers: headers, body: body, limit: Int(limit), timeout: timeout,
      tls: tls)
  }
  /// Stores TLS and socket options: 1, 0 when they do not parse, -1 on allocation failure.
  static func configure(_ native: OpaquePointer, _ tls: GHTTPTLS) -> Int32 {
    let none: [UInt8] = []
    var status = (tls.cert ?? none).withUnsafeBufferPointer { c in
      (tls.key ?? none).withUnsafeBufferPointer { k in
        (tls.roots ?? none).withUnsafeBufferPointer { r in
          gcn_http_tls(
            native, tls.cert == nil ? nil : (c.baseAddress ?? UnsafePointer(bitPattern: 1)),
            c.count, k.baseAddress, k.count,
            tls.roots == nil ? nil : (r.baseAddress ?? UnsafePointer(bitPattern: 1)), r.count,
            tls.insecure ? 1 : 0)
        }
      }
    }
    if status == 1, let socket = tls.socket {
      let stored = (socket + [0]).withUnsafeBufferPointer { p in
        p.baseAddress!.withMemoryRebound(to: CChar.self, capacity: p.count) { gcn_http_socket(native, $0) }
      }
      if stored != 1 { status = -1 }
    }
    return status
  }
  /// Checks a URL and alternating headers as Do does.
  static func check(_ url: [UInt8], _ headers: [[UInt8]]) throws {
    if headers.count % 2 != 0 || url.count > 8192
      || url.contains(where: { $0 <= 32 || $0 == 127 || $0 == 92 })
    {
      throw GReject("http: invalid request or limit")
    }
    guard let raw = String(bytes: url, encoding: .utf8),
      let components = URLComponents(string: raw), let scheme = components.scheme,
      ["http", "https"].contains(scheme), let host = components.host, !host.isEmpty,
      components.user == nil, components.password == nil, components.fragment == nil,
      components.port == nil || (components.port! > 0 && components.port! <= 65535)
    else { throw GReject("http: invalid request or limit") }
    let forbidden = [
      "host", "content-length", "transfer-encoding", "connection", "proxy-authorization",
      "proxy-connection", "upgrade", "trailer", "te",
    ]
    let allowed = Set(
      Array("!#$%&'*+-.^_`|~0123456789ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz".utf8))
    var total = 0
    for i in stride(from: 0, to: headers.count, by: 2) {
      let name = headers[i]
      let value = headers[i + 1]
      total += name.count + value.count + 4
      if name.isEmpty || name.contains(where: { !allowed.contains($0) })
        || value.contains(where: { $0 == 127 || ($0 < 32 && $0 != 9) }) || total > 65536
        || forbidden.contains(String(decoding: name, as: UTF8.self).lowercased())
      {
        throw GReject("http: invalid request or limit")
      }
    }
  }
  static func options(_ options: [[UInt8]]) throws -> GHTTPTLS? {
    if options.isEmpty { return nil }
    if options.count % 2 != 0 { throw GReject("http: invalid request or limit") }
    var values: [String: [UInt8]] = [:]
    var total = 0
    for i in stride(from: 0, to: options.count, by: 2) {
      let name = String(decoding: options[i], as: UTF8.self)
      total += options[i].count + options[i + 1].count
      if total > 1 << 20 || values[name] != nil
        || !["client-cert", "client-key", "root-cas", "insecure-skip-verify", "unix-socket"].contains(name)
        || Array(name.utf8) != options[i]
      {
        throw GReject("http: invalid request or limit")
      }
      values[name] = options[i + 1]
    }
    let skip = values["insecure-skip-verify"]
    let socket = values["unix-socket"]
    if (values["client-cert"] == nil) != (values["client-key"] == nil)
      || (skip != nil && skip! != Array("true".utf8) && skip! != Array("false".utf8))
      || (socket != nil && (socket!.isEmpty || socket!.count > 4096 || socket!.contains(0)))
    {
      throw GReject("http: invalid request or limit")
    }
    return GHTTPTLS(
      cert: values["client-cert"], key: values["client-key"], roots: values["root-cas"],
      insecure: skip == Array("true".utf8), socket: socket)
  }
  static func start(_ args: [GValue], _ task: GTask) throws -> GAction {
    let owner = task.owner
    let context: GContext
    do { context = try GNative.context(args[0], owner) } catch {
      return .resume(zero(GNative.error("http: invalid request or limit")))
    }
    if case .nilValue = context.error {} else { return .resume(zero(context.error)) }
    let input: GHTTPInput
    do { input = try validate(args) } catch {
      return .resume(zero(GNative.error("http: invalid request or limit")))
    }
    guard let native = gcn_http_create() else { throw GFault("HTTP allocation failure") }
    if let tls = input.tls {
      let status = configure(native, tls)
      if status != 1 {
        gcn_http_release(native)
        if status < 0 { throw GFault("HTTP allocation failure") }
        return .resume(zero(GNative.error("http: invalid TLS option")))
      }
    }
    let operation = owner.newOperation(task)
    operation.retained = args
    operation.cancelNative = { gcn_http_cancel(native) }
    let hook = UUID()
    context.hooks[hook] = { operation.cancel() }
    let timer = owner.timer(input.timeout * 1_000_000) {
      operation.timedOut = true
      operation.cancel()
    }
    DispatchQueue.global().async {
      let output = work(native, input)
      owner.mailbox.publish {
        timer.active = false
        context.hooks[hook] = nil
        operation.cancelNative = nil
        gcn_http_release(native)
        let values: [GValue]
        if case .nilValue = context.error {
          if operation.timedOut {
            values = zero(GNative.deadlineError)
          } else if let error = output.error {
            values = zero(GNative.error(error))
          } else {
            let elem =
              GTypes.table.firstIndex { $0.kind == "string" }
              ?? GNative.addPrimitive("string", "string", 0, false)
            let b = GBuffer(output.headers.count, elem)
            for i in 0..<output.headers.count { b.write(i, .string(output.headers[i])) }
            values = [
              .int(output.status), .slice(GSlice(b, 0, b.count, b.count, elem)),
              GNative.bytes(output.body), .nilValue,
            ]
          }
        } else {
          values = zero(context.error)
        }
        owner.finish(operation, values)
      }
    }
    return .park
  }
  static func work(_ native: OpaquePointer, _ input: GHTTPInput) -> GHTTPOutput {
    func cString(_ bytes: [UInt8]) -> UnsafeMutablePointer<CChar> {
      let p = UnsafeMutablePointer<CChar>.allocate(capacity: bytes.count + 1)
      for i in 0..<bytes.count { p[i] = CChar(bitPattern: bytes[i]) }
      p[bytes.count] = 0
      return p
    }
    let method = cString(input.method)
    let url = cString(input.url)
    let headers = input.headers.map(cString)
    defer {
      method.deallocate()
      url.deallocate()
      for header in headers { header.deallocate() }
    }
    let pointers = headers.map { Optional(UnsafePointer($0)) }
    let ok = pointers.withUnsafeBufferPointer { h in
      input.body.withUnsafeBufferPointer { b in
        gcn_http_run(
          native, method, url, h.baseAddress, h.count, b.baseAddress, b.count, input.limit,
          input.timeout)
      }
    }
    if ok != 1 {
      return GHTTPOutput(
        status: 0, headers: [], body: [], error: String(cString: gcn_http_error(native)))
    }
    var count = 0
    let body = gcn_http_body(native, &count)
    var output: [[UInt8]] = []
    for i in 0..<gcn_http_headers(native) {
      let name = gcn_http_header_name(native, i)!
      let value = gcn_http_header_value(native, i)!
      output.append(Array(UnsafeRawBufferPointer(start: name, count: strlen(name))))
      output.append(Array(UnsafeRawBufferPointer(start: value, count: strlen(value))))
    }
    return GHTTPOutput(
      status: Int64(gcn_http_status(native)), headers: output,
      body: Array(UnsafeBufferPointer(start: body, count: count)), error: nil)
  }
}

final class GProviderLease {
  let lock = NSLock()
  var settled = false
  var cancellationHook: (() -> Void)?
  var canceled = false
  func cancel() {
    lock.lock()
    if canceled || settled {
      lock.unlock()
      return
    }
    canceled = true
    let hook = cancellationHook
    lock.unlock()
    schedule(hook)
  }
  func install(_ hook: (() -> Void)?) {
    lock.lock()
    if settled {
      lock.unlock()
      return
    }
    cancellationHook = hook
    let canceled = canceled
    lock.unlock()
    if canceled { schedule(hook) }
  }
  private func schedule(_ hook: (() -> Void)?) {
    guard let hook = hook else { return }
    DispatchQueue.global().async {
      self.lock.lock()
      let settled = self.settled
      self.lock.unlock()
      if !settled { GLibrary.provider { hook() } }
    }
  }
  func terminal() -> Bool {
    lock.lock()
    defer { lock.unlock() }
    if settled { return false }
    settled = true
    cancellationHook = nil
    return true
  }
}
enum GCallback {
  static func start(_ args: [GValue], _ task: GTask) throws -> GAction {
    let owner = task.owner
    let context: GContext
    do { context = try GNative.context(args[0], owner) } catch {
      return .resume([GNative.nilBytes(), GNative.error("callback: invalid request")])
    }
    if case .nilValue = context.error {
    } else {
      return .resume([GNative.nilBytes(), context.error])
    }
    let name = args[1].bytes
    let body = try GNative.buffer(args[2])
    guard !name.isEmpty, name.count <= 128, body.count <= 1 << 20,
      let text = String(bytes: name, encoding: .utf8)
    else { return .resume([GNative.nilBytes(), GNative.error("callback: invalid request")]) }
    guard let provider = owner.callbacks[text] else {
      return .resume([GNative.nilBytes(), GNative.error("callback: unavailable")])
    }
    let token = CancellationToken()
    let lease = GProviderLease()
    let operation = owner.newOperation(task)
    let hook = UUID()
    operation.retained = args
    operation.cancelNative = {
      token.cancel()
      lease.cancel()
    }
    context.hooks[hook] = { operation.cancel() }
    DispatchQueue.global().async {
      Thread.current.threadDictionary["goalchemy.provider"] = true
      defer { Thread.current.threadDictionary.removeObject(forKey: "goalchemy.provider") }
      let cancel = provider(CallbackRequest(bytes: body, cancellation: token)) { result in
        if !lease.terminal() { return }
        let reply: [UInt8]
        let failure: String?
        switch result {
        case .success(let bytes):
          reply = bytes
          failure = nil
        case .failure(let error):
          reply = []
          failure = GLibrary.provider { String(describing: error) }
        }
        owner.mailbox.publish {
          context.hooks[hook] = nil
          operation.cancelNative = nil
          let values: [GValue]
          if case .nilValue = context.error {
            if let failure = failure {
              values = [GNative.nilBytes(), GNative.error(failure)]
            } else {
              values =
                reply.count <= 1 << 20
                ? [GNative.bytes(reply), .nilValue]
                : [GNative.nilBytes(), GNative.error("callback: reply limit")]
            }
          } else {
            values = [GNative.nilBytes(), context.error]
          }
          owner.finish(operation, values)
        }
      }
      lease.install(cancel)
    }
    return .park
  }
}

/// A lib/http exchange: the native transfer runs on its own thread, and each
/// operation blocks a worker until the transfer can answer.
final class GHTTPExchange: GOpaque {
  let native: OpaquePointer
  let context: GContext
  let deadline: UInt64
  let length: Int64
  var written: Int64 = 0
  var responded = false
  var closed = false
  init(_ native: OpaquePointer, _ context: GContext, _ deadline: UInt64, _ length: Int64) {
    self.native = native
    self.context = context
    self.deadline = deadline
    self.length = length
    super.init()
  }
  func close() {
    if closed { return }
    closed = true
    gcn_exchange_abort(native)
  }
  deinit {
    gcn_exchange_abort(native)
    gcn_exchange_release(native)
  }
}
/// A worker's answer, converted to source values on the owner.
enum GHTTPPayload {
  case done
  case head(Int64, [UInt8])
  case data([UInt8])
  case eof
  case failure(String)
}
enum GHTTPExchanges {
  static let closedError = "http: exchange closed"
  static let stateError = "http: invalid exchange operation"
  static let invalidError = "http: invalid request or limit"
  static func exchange(_ value: GValue) -> GHTTPExchange? {
    guard case .pointer(let cell) = value, case .opaque(let o) = cell.value else { return nil }
    return o as? GHTTPExchange
  }
  static func open(_ args: [GValue], _ task: GTask) throws -> GAction {
    let fail = { (e: GValue) -> GAction in .resume([.nilValue, e]) }
    let context: GContext
    do { context = try GNative.context(args[0], task.owner) } catch {
      return fail(GNative.error(invalidError))
    }
    if case .nilValue = context.error {} else { return fail(context.error) }
    let method = args[1].bytes
    let url = args[2].bytes
    let length = args[4].intValue
    let timeout = args[5].intValue
    let methodText = String(decoding: method, as: UTF8.self)
    let headers: [[UInt8]]
    let tls: GHTTPTLS?
    do {
      headers = try GElements(args[3]).map { $0.bytes }
      if (methodText != "GET" && methodText != "POST") || length < -1
        || (methodText == "GET" && length != 0) || timeout < 1 || timeout > 86_400_000
      {
        throw GReject(invalidError)
      }
      try GHTTP.check(url, headers)
      tls = try GHTTP.options(GElements(args[6]).map { $0.bytes })
    } catch {
      return fail(GNative.error(invalidError))
    }
    guard let transport = gcn_http_create() else { throw GFault("HTTP allocation failure") }
    if let tls = tls {
      let status = GHTTP.configure(transport, tls)
      if status != 1 {
        gcn_http_release(transport)
        if status < 0 { throw GFault("HTTP allocation failure") }
        return fail(GNative.error("http: invalid TLS option"))
      }
    }
    func cString(_ bytes: [UInt8]) -> [CChar] { bytes.map { CChar(bitPattern: $0) } + [0] }
    let strings = [cString(method), cString(url)] + headers.map(cString)
    var pointers: [UnsafeMutablePointer<CChar>] = []
    for s in strings {
      let p = UnsafeMutablePointer<CChar>.allocate(capacity: s.count)
      p.initialize(from: s, count: s.count)
      pointers.append(p)
    }
    defer { for p in pointers { p.deallocate() } }
    let headerPointers = pointers[2...].map { Optional(UnsafePointer($0)) }
    let native = headerPointers.withUnsafeBufferPointer { h in
      gcn_exchange_open(transport, pointers[0], pointers[1], h.baseAddress, h.count, length)
    }
    guard let native = native else {
      gcn_http_release(transport)
      throw GFault("HTTP exchange submission failure")
    }
    let deadline = DispatchTime.now().uptimeNanoseconds + UInt64(timeout) * 1_000_000
    return .resume([.pointer(GCell(.opaque(GHTTPExchange(native, context, deadline, length)))), .nilValue])
  }
  /// Runs work on a worker; it returns values or an error message. The
  /// remaining exchange deadline and the context cancel the operation.
  static func operation(
    _ task: GTask, _ x: GHTTPExchange, _ zero: @escaping (GValue) -> [GValue],
    _ work: @escaping () -> GHTTPPayload
  ) -> GAction {
    let owner = task.owner
    let now = DispatchTime.now().uptimeNanoseconds
    if now >= x.deadline {
      gcn_exchange_abort(x.native)
      return .resume(zero(GNative.deadlineError))
    }
    if case .nilValue = x.context.error {} else {
      gcn_exchange_abort(x.native)
      return .resume(zero(x.context.error))
    }
    let operation = owner.newOperation(task)
    let native = x.native
    gcn_exchange_retain(native)
    operation.cancelNative = { gcn_exchange_abort(native) }
    let hook = UUID()
    x.context.hooks[hook] = { operation.cancel() }
    let timer = owner.timer(Int64(x.deadline - now)) {
      operation.timedOut = true
      operation.cancel()
    }
    DispatchQueue.global().async {
      let result = work()
      gcn_exchange_release(native)
      owner.mailbox.publish {
        timer.active = false
        x.context.hooks[hook] = nil
        operation.cancelNative = nil
        let values: [GValue]
        if case .nilValue = x.context.error {
          if operation.timedOut {
            values = zero(GNative.deadlineError)
          } else {
            switch result {
            case .done: values = [.nilValue]
            case .head(let status, let raw): values = [.int(status), headerSlice(raw), .nilValue]
            case .data(let bytes): values = [GNative.bytes(bytes), .bool(false), .nilValue]
            case .eof: values = [GNative.nilBytes(), .bool(true), .nilValue]
            case .failure(let e): values = zero(GNative.error(e))
            }
          }
        } else {
          values = zero(x.context.error)
        }
        owner.finish(operation, values)
      }
    }
    return .park
  }
  static func write(_ args: [GValue], _ task: GTask) throws -> GAction {
    let zero = { (e: GValue) -> [GValue] in [e] }
    guard let x = exchange(args[0]), let data = try? GNative.buffer(args[1]), data.count <= 1 << 20 else {
      return .resume(zero(GNative.error(invalidError)))
    }
    if x.closed { return .resume(zero(GNative.error(closedError))) }
    if x.responded || x.length == 0 && !data.isEmpty || x.length >= 0 && Int64(data.count) > x.length - x.written {
      return .resume(zero(GNative.error(stateError)))
    }
    x.written += Int64(data.count)
    if data.isEmpty { return .resume([.nilValue]) }
    let native = x.native
    return operation(task, x, zero) {
      let ok = data.withUnsafeBufferPointer { gcn_exchange_write(native, $0.baseAddress, $0.count) }
      return ok == 1 ? .done : .failure(String(cString: gcn_exchange_error(native)))
    }
  }
  static func response(_ args: [GValue], _ task: GTask) throws -> GAction {
    let zero = { (e: GValue) -> [GValue] in [.int(0), GNative.nilStrings(), e] }
    guard let x = exchange(args[0]) else { return .resume(zero(GNative.error(invalidError))) }
    if x.closed { return .resume(zero(GNative.error(closedError))) }
    x.responded = true
    if x.length >= 0 && x.written != x.length {
      gcn_exchange_abort(x.native)
      return .resume(zero(GNative.error(stateError)))
    }
    let native = x.native
    return operation(task, x, zero) {
      if gcn_exchange_response(native) != 1 { return .failure(String(cString: gcn_exchange_error(native))) }
      var size = 0
      let head = gcn_exchange_head(native, &size)!
      let raw = Array(UnsafeRawBufferPointer(start: head, count: size))
      return .head(Int64(gcn_exchange_status(native)), raw)
    }
  }
  /// Sorted canonical headers of a raw response head, without
  /// Transfer-Encoding and Connection: close, as Go reports them.
  static func headerSlice(_ raw: [UInt8]) -> GValue {
    var pairs: [([UInt8], [UInt8], Int)] = []
    for line in raw.split(separator: 10, omittingEmptySubsequences: true) {
      var line = Array(line)
      if line.last == 13 { line.removeLast() }
      guard let colon = line.firstIndex(of: 58) else { continue }
      var name = Array(line[..<colon])
      var value = Array(line[(colon + 1)...])
      while let f = value.first, f == 32 || f == 9 { value.removeFirst() }
      while let l = value.last, l == 32 || l == 9 { value.removeLast() }
      var upper = true
      for i in 0..<name.count {
        let c = name[i]
        name[i] = upper ? (c >= 97 && c <= 122 ? c - 32 : c) : (c >= 65 && c <= 90 ? c + 32 : c)
        upper = c == 45
      }
      let text = String(decoding: name, as: UTF8.self)
      if text == "Transfer-Encoding" { continue }
      if text == "Connection" && String(decoding: value, as: UTF8.self).lowercased().split(separator: ",").contains(where: { $0.trimmingCharacters(in: .whitespaces) == "close" }) { continue }
      pairs.append((name, value, pairs.count))
    }
    pairs.sort { $0.0 != $1.0 ? $0.0.lexicographicallyPrecedes($1.0) : $0.2 < $1.2 }
    let elem = GTypes.table.firstIndex { $0.kind == "string" } ?? GNative.addPrimitive("string", "string", 0, false)
    let b = GBuffer(pairs.count * 2, elem)
    for (i, p) in pairs.enumerated() {
      b.write(2 * i, .string(p.0))
      b.write(2 * i + 1, .string(p.1))
    }
    return .slice(GSlice(b, 0, b.count, b.count, elem))
  }
  static func read(_ args: [GValue], _ task: GTask) throws -> GAction {
    let zero = { (e: GValue) -> [GValue] in [GNative.nilBytes(), .bool(false), e] }
    let max = args[1].intValue
    guard let x = exchange(args[0]), max >= 1, max <= 1 << 20 else { return .resume(zero(GNative.error(invalidError))) }
    if x.closed { return .resume(zero(GNative.error(closedError))) }
    if !x.responded { return .resume(zero(GNative.error(stateError))) }
    let native = x.native
    return operation(task, x, zero) {
      var buffer = [UInt8](repeating: 0, count: Int(max))
      let n = buffer.withUnsafeMutableBufferPointer { gcn_exchange_read(native, $0.baseAddress, $0.count) }
      if n < 0 { return .failure(String(cString: gcn_exchange_error(native))) }
      if n == 0 { return .eof }
      return .data(Array(buffer[..<n]))
    }
  }
  static func close(_ args: [GValue]) -> [GValue] {
    exchange(args[0])?.close()
    return []
  }
}
