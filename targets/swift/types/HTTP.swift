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
    let tls = args.count > 7 ? try options(GElements(args[7]).map { $0.bytes }) : nil
    return GHTTPInput(
      method: method, url: url, headers: headers, body: body, limit: Int(limit), timeout: timeout,
      tls: tls)
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
        || !["client-cert", "client-key", "root-cas", "insecure-skip-verify"].contains(name)
        || Array(name.utf8) != options[i]
      {
        throw GReject("http: invalid request or limit")
      }
      values[name] = options[i + 1]
    }
    let skip = values["insecure-skip-verify"]
    if (values["client-cert"] == nil) != (values["client-key"] == nil)
      || (skip != nil && skip! != Array("true".utf8) && skip! != Array("false".utf8))
    {
      throw GReject("http: invalid request or limit")
    }
    return GHTTPTLS(
      cert: values["client-cert"], key: values["client-key"], roots: values["root-cas"],
      insecure: skip == Array("true".utf8))
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
      let none: [UInt8] = []
      let status = (tls.cert ?? none).withUnsafeBufferPointer { c in
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
