// SPDX-License-Identifier: Apache-2.0
import Foundation

enum GNative {
  static let canceledError = error("context canceled")
  static let deadlineError = GValue.interface(
    GInterface(-1, .error(GError("context deadline exceeded", "context.deadlineExceededError"))))
  static var background: GContext?
  static func error(_ message: String) -> GValue {
    .interface(GInterface(-1, .error(GError(message))))
  }
  static func bytes(_ bytes: [UInt8]) -> GValue {
    let elem =
      GTypes.table.firstIndex { $0.kind == "int" && $0.bits == 8 && !$0.signed }
      ?? addPrimitive("uint8", "int", 8, false)
    let b = GBuffer(bytes: bytes, elem: elem)
    return .slice(GSlice(b, 0, bytes.count, bytes.count, elem))
  }
  static func nilBytes() -> GValue {
    let elem =
      GTypes.table.firstIndex { $0.kind == "int" && $0.bits == 8 && !$0.signed }
      ?? addPrimitive("uint8", "int", 8, false)
    return .slice(GSlice(nil, 0, 0, 0, elem))
  }
  static func nilStrings() -> GValue {
    let elem =
      GTypes.table.firstIndex { $0.kind == "string" } ?? addPrimitive("string", "string", 0, false)
    return .slice(GSlice(nil, 0, 0, 0, elem))
  }
  static func strings(_ values: [String]) -> GValue {
    let elem =
      GTypes.table.firstIndex { $0.kind == "string" } ?? addPrimitive("string", "string", 0, false)
    let b = GBuffer(values.count, elem)
    for i in 0..<values.count { b.write(i, .text(values[i])) }
    return .slice(GSlice(b, 0, values.count, values.count, elem))
  }
  static func addPrimitive(_ name: String, _ kind: String, _ bits: Int, _ signed: Bool) -> Int {
    let id = GTypes.table.count
    GTypes.table.append(GType(name, kind, bits, signed, -1, -1, 0, [], [], [], [], true))
    return id
  }
  static func buffer(_ value: GValue) throws -> [UInt8] {
    if case .slice(let s) = value, let bytes = s.storage?.bytes {
      return Array(bytes[s.offset..<s.offset + s.length])
    }
    if case .slice(let s) = value, s.length == 0 { return [] }
    throw GFault("expected native byte slice")
  }
  static func context(_ value: GValue, _ owner: GOwner) throws -> GContext {
    guard case .opaque(let opaque) = value, let c = opaque as? GContext else {
      throw GPanic.runtime("invalid memory address or nil pointer dereference")
    }
    if let existing = c.owner, existing !== owner || !existing.active {
      throw GFault("foreign source context")
    }
    return c
  }
  static func frame(_ contract: String, _ args: [GValue], _ types: [Int] = []) -> GFrame {
    if contract == "std.errors.is" { return isFrame(args) }
    if contract == "std.errors.unwrap" {
      if case .interface(let b) = args[0], b.type >= 0,
        GTypes.table[b.type].nativeMethods.contains("Unwrap")
      {
        do { return try GInterface.start(args[0], "Unwrap", []) } catch {
          return GFrame.failure(error)
        }
      }
    }
    let frame = GFrame(-1)
    frame.roots = { args }
    frame.step = { frame, task in
      if frame.pc == 1 { return .complete(task.rv) }
      frame.pc = 1
      switch contract {
      case "core.chan.make": return .resume([try GChannel.make(args[0], types[0])])
      case "core.chan.send": return try GChannel.send(args[0], args[1], task)
      case "core.chan.recv": return try GChannel.receive(args[0], task)
      case "core.chan.close":
        try GChannel.close(args[0], task.owner)
        return .resume([])
      case "core.chan.len": return .resume([try GLength(args[0], false)])
      case "core.chan.cap": return .resume([try GLength(args[0], true)])
      case "core.select":
        return try GChannel.select(
          [GSelectCase(args[0], false, .nilValue), GSelectCase(args[1], false, .nilValue)],
          args[2].boolValue, task)
      case "core.task.spawn":
        var ran = false
        task.owner.spawn(
          GFrame.sync {
            ran = true
            return []
          })
        let before = ran
        frame.step = { _, _ in
          if !ran { throw GFault("spawn did not run after yield") }
          return .complete([.bool(before)])
        }
        return .yield
      case "std.runtime.gosched": return .yield
      case "std.time.sleep":
        let delay = args[0].intValue
        if delay <= 0 { return .resume([]) }
        let timer = task.owner.timer(delay) { task.owner.ready(task, []) }
        task.cleanup = { timer.active = false }
        return .park
      case "std.sync.mutex.lock":
        guard case .pointer(let p) = args[0], case .opaque(let o) = p.value, let m = o as? GMutex
        else { throw GPanic.runtime("invalid memory address or nil pointer dereference") }
        return m.lock(task)
      case "std.sync.waitgroup.wait":
        guard case .pointer(let p) = args[0], case .opaque(let o) = p.value,
          let w = o as? GWaitGroup
        else { throw GPanic.runtime("invalid memory address or nil pointer dereference") }
        return w.wait(task)
      case "lib.task.all":
        let fns = try GElements(args[0])
        if fns.isEmpty { return .resume([]) }
        var count = fns.count
        for fn in fns {
          let child = GFrame(-1)
          child.step = { f, t in
            if f.pc == 0 {
              f.pc = 1
              return .call(try GFunction.start(fn, []))
            }
            count -= 1
            if count == 0 { task.owner.ready(task, []) }
            return .complete([])
          }
          task.owner.spawn(child)
        }
        return .park
      case "lib.http.do", "lib.http.do_with": return try GHTTP.start(args, task)
      case "lib.http.open": return try GHTTPExchanges.open(args, task)
      case "lib.http.exchange_write": return try GHTTPExchanges.write(args, task)
      case "lib.http.exchange_response": return try GHTTPExchanges.response(args, task)
      case "lib.http.exchange_read": return try GHTTPExchanges.read(args, task)
      case "lib.http.exchange_close": return .resume(GHTTPExchanges.close(args))
      case "lib.callback.request": return try GCallback.start(args, task)
      default:
        if contract.hasPrefix("lib.crypto.") { return try GCrypto.start(contract, args, task) }
        return .resume(try invoke(contract, args, types))
      }
    }
    return frame
  }
  static func isFrame(_ args: [GValue]) -> GFrame {
    if case .nilValue = args[1] {
      let frame = GFrame.sync { [.bool(try GEqual(args[0], .nilValue))] }
      frame.roots = { args }
      return frame
    }
    let frame = GFrame(-1)
    var current = args[0]
    let target = args[1]
    frame.roots = { args + [current, target] }
    frame.step = { frame, task in
      while true {
        if frame.pc == 1 {
          if task.rv.first?.boolValue == true { return .complete([.bool(true)]) }
          frame.pc = 2
        }
        if frame.pc == 3 {
          current = task.rv[0]
          frame.pc = 0
        }
        if frame.pc == 0 {
          let comparable: Bool
          if case .interface(let box) = target, box.type >= 0 {
            comparable = GTypes.table[box.type].comparable
          } else {
            comparable = true
          }
          if comparable, try GEqual(current, target) { return .complete([.bool(true)]) }
          guard case .interface(let b) = current, b.type >= 0 else {
            return .complete([.bool(false)])
          }
          if GTypes.table[b.type].nativeMethods.contains("Is") {
            frame.pc = 1
            return .call(try GInterface.start(current, "Is", [target]))
          }
          frame.pc = 2
        }
        if frame.pc == 2 {
          guard case .interface(let b) = current, b.type >= 0,
            GTypes.table[b.type].nativeMethods.contains("Unwrap")
          else { return .complete([.bool(false)]) }
          frame.pc = 3
          return .call(try GInterface.start(current, "Unwrap", []))
        }
      }
    }
    return frame
  }
  static func invoke(_ contract: String, _ args: [GValue], _ types: [Int] = []) throws -> [GValue] {
    if contract.hasPrefix("core.integer.") || contract.hasPrefix("core.float.") {
      let operation = contract.components(separatedBy: ".").last!
      if operation == "convert" {
        let t = GTypes.table[types.last!]
        return [GNumeric.convert(args[0], t.bits, t.signed, t.kind == "float")]
      }
      if operation == "neg" { return [try GNumeric.unary("neg", args[0])] }
      if operation == "not" { return [try GNumeric.unary("bitnot", args[0])] }
      if operation == "compare" {
        return [
          .int(
            try GEqual(args[0], args[1])
              ? 0 : (try GNumeric.binary("<", args[0], args[1])).boolValue ? -1 : 1)
        ]
      }
      let ops = [
        "add": "+", "sub": "-", "mul": "*", "div": "/", "rem": "%", "and": "&", "or": "|",
        "xor": "^", "andnot": "&^", "shl": "<<", "shr": ">>", "min": "min", "max": "max",
      ]
      return [try GNumeric.binary(ops[operation]!, args[0], args[1])]
    }
    switch contract {
    case "core.string.concat": return [.string(args[0].bytes + args[1].bytes)]
    case "core.string.index": return [try GString.index(args[0], args[1])]
    case "core.string.slice": return [try GSlice.reslice(args[0], args[1], args[2], nil)]
    case "core.string.compare":
      return [
        .int(
          args[0].bytes == args[1].bytes
            ? 0 : args[0].bytes.lexicographicallyPrecedes(args[1].bytes) ? -1 : 1)
      ]
    case "core.string.decode_rune":
      let (r, w) = try GString.decode(args[0], args[1])
      return [r, w]
    case "core.string.from_rune": return [.string(GString.rune(args[0]))]
    case "core.string.from_bytes": return [.string(try buffer(args[0]))]
    case "core.string.to_bytes": return [bytes(args[0].bytes)]
    case "core.string.from_runes":
      return [.string(try GElements(args[0]).flatMap { GString.rune($0) })]
    case "core.string.to_runes": return [try GConvert(args[0], types[0], 6)]
    case "core.slice.make": return [try GSlice.make(args[0], args[1], types[0])]
    case "core.slice.index": return [GCopy(try GSlice.cell(args[0], args[1]).value)]
    case "core.slice.store":
      GStore(try GSlice.cell(args[0], args[1]), args[2])
      return []
    case "core.slice.slice":
      return [try GSlice.reslice(args[0], args[1], args[2], args.count > 3 ? args[3] : nil)]
    case "core.slice.append": return [try GSlice.append(args[0], args[1])]
    case "core.slice.copy": return [try GSlice.copy(args[0], args[1])]
    case "core.slice.clear":
      try GClear(args[0])
      return []
    case "core.slice.to_array": return [try GConvert(args[0], types[0], 8)]
    case "core.map.make": return [.map(GMap(types[0], types[1]))]
    case "core.map.lookup":
      let result = try GMap.lookup(args[0], args[1], types.last!)
      return [result.0, .bool(result.1)]
    case "core.map.store":
      try GMap.store(args[0], args[1], args[2])
      return []
    case "core.map.delete":
      try GMap.delete(args[0], args[1])
      return []
    case "core.map.clear":
      try GClear(args[0])
      return []
    case "core.map.len": return [try GLength(args[0], false)]
    case "core.map.iterate":
      if case .map(let map) = args[0] {
        let keys = map.entries.filter { $0.live }.map { GCopy($0.key) }
        let buffer = GBuffer(keys.count, map.key)
        for i in 0..<keys.count { buffer.write(i, keys[i]) }
        return [.slice(GSlice(buffer, 0, keys.count, keys.count, map.key))]
      }
      return [GTypes.zero(types[0])]
    case "core.print": return [.string(GFormat(args[0]))]
    case "std.errors.new": return [.interface(GInterface(-1, .error(GError(bytes: args[0].bytes))))]
    case "std.errors.unwrap": return [.nilValue]
    case "std.errors.is": return [.bool(try GEqual(args[0], args[1]))]
    case "std.sync.mutex.unlock":
      guard case .pointer(let p) = args[0], case .opaque(let o) = p.value, let m = o as? GMutex
      else { throw GPanic.runtime("invalid memory address or nil pointer dereference") }
      try m.unlock()
      return []
    case "std.sync.waitgroup.add", "std.sync.waitgroup.done":
      guard case .pointer(let p) = args[0], case .opaque(let o) = p.value, let w = o as? GWaitGroup
      else { throw GPanic.runtime("invalid memory address or nil pointer dereference") }
      try w.add(contract.hasSuffix("done") ? -1 : args[1].intValue)
      return []
    case "std.context.background":
      if background == nil {
        let elem =
          GTypes.table.firstIndex { $0.kind == "struct" && $0.fields.isEmpty }
          ?? addPrimitive("struct {}", "struct", 0, false)
        background = GContext(elem)
      }
      return [.opaque(background!)]
    case "std.context.with_cancel", "std.context.with_timeout":
      guard let owner = GOwner.current else { throw GFault("context requires owner") }
      let p = try context(args[0], owner)
      let c = GContext(p.done.elem)
      c.owner = owner
      c.parent = p
      if p !== background { p.descendants.append(c) }
      if case .nilValue = p.error {} else { c.cancel(p.errorErrorIsDeadline) }
      if contract.hasSuffix("timeout") {
        if args[1].intValue <= 0 {
          c.cancel(true)
        } else {
          c.timer = owner.timer(args[1].intValue, retained: [.opaque(c)]) { c.cancel(true) }
        }
      }
      let cancel = GFunction(
        -1,
        { _, _ in
          GFrame.sync {
            c.cancel()
            return []
          }
        })
      cancel.retained = [.opaque(c)]
      return [.opaque(c), .function(cancel)]
    case "std.context.err":
      guard let owner = GOwner.current else { throw GFault("context requires owner") }
      return [try context(args[0], owner).error]
    case "std.context.done":
      guard let owner = GOwner.current else { throw GFault("context requires owner") }
      let c = try context(args[0], owner)
      return [c === background ? .nilValue : .channel(c.done)]
    case "std.context.canceled": return [canceledError]
    case "std.context.deadline_exceeded": return [deadlineError]
    case "lib.clock.unix": return [.int(Int64(Date().timeIntervalSince1970))]
    case "lib.clock.unix_nano":
      var ts = timespec()
      guard clock_gettime(CLOCK_REALTIME, &ts) == 0 else { throw GFault("UTC clock failure") }
      return [.int(Int64(ts.tv_sec) * 1_000_000_000 + Int64(ts.tv_nsec))]
    case "lib.log.enabled": return [.bool(GLog.enabled(args[0].intValue))]
    case "lib.log.emit":
      GLog.emit(args)
      return []
    case "lib.os.read_file": return GFiles.readFile(args[0])
    case "lib.os.write_file": return try GFiles.writeFile(args[0], args[1], args[2])
    case "lib.os.open": return GFiles.openHandle(args[0], args[1], args[2])
    case "lib.os.read": return GFiles.readHandle(args[0], args[1])
    case "lib.os.read_at": return GFiles.readAtHandle(args[0], args[1], args[2])
    case "lib.os.write": return try GFiles.writeHandle(args[0], args[1])
    case "lib.os.seek": return GFiles.seekHandle(args[0], args[1], args[2])
    case "lib.os.sync": return GFiles.syncHandle(args[0])
    case "lib.os.close": return GFiles.closeHandle(args[0])
    case "lib.os.stat": return GFiles.statPath(args[0])
    case "lib.os.create_temp": return GFiles.createTemp(args[0], args[1], args[2])
    case "lib.os.remove": return GFiles.removePath(args[0])
    case "lib.os.rename": return GFiles.renamePath(args[0], args[1])
    default:
      if contract.hasPrefix("lib.encoding.") { return try encoding(contract, args) }
      if contract == "lib.checksum.crc32_ieee" { return [try GCrypto.crc32(args[0])] }
      if contract.hasPrefix("lib.crypto.") { return try GCrypto.invoke(contract, args) }
      throw GFault("unimplemented contract " + contract)
    }
  }
  static func encoding(_ contract: String, _ args: [GValue]) throws -> [GValue] {
    let url = contract.contains("url")
    let encode = contract.hasSuffix("encode")
    if encode {
      let input = try buffer(args[0])
      if input.count > 64 << 20 { return [.text(""), error("encoding: invalid input or size")] }
      var text = Data(input).base64EncodedString()
      if url {
        text = text.replacingOccurrences(of: "+", with: "-").replacingOccurrences(
          of: "/", with: "_"
        ).replacingOccurrences(of: "=", with: "")
      }
      return [.text(text), .nilValue]
    }
    let original = args[0].bytes
    if original.count > ((64 << 20) + 2) / 3 * 4
      || original.contains(where: { $0 == 32 || $0 == 9 || $0 == 10 || $0 == 13 })
    {
      return [nilBytes(), error("encoding: invalid input or size")]
    }
    var text = String(decoding: original, as: UTF8.self)
    if url {
      text = text.replacingOccurrences(of: "-", with: "+").replacingOccurrences(of: "_", with: "/")
      text += String(repeating: "=", count: (4 - text.count % 4) % 4)
    }
    guard let data = Data(base64Encoded: text), data.count <= 64 << 20 else {
      return [nilBytes(), error("encoding: invalid input or size")]
    }
    var canonical = data.base64EncodedString()
    if url {
      canonical = canonical.replacingOccurrences(of: "+", with: "-").replacingOccurrences(
        of: "/", with: "_"
      ).replacingOccurrences(of: "=", with: "")
    }
    if Array(canonical.utf8) != original {
      return [nilBytes(), error("encoding: invalid input or size")]
    }
    return [bytes(Array(data)), .nilValue]
  }
}
extension GContext {
  var errorErrorIsDeadline: Bool {
    if case .interface(let b) = error, case .error(let e) = b.value {
      return e.message == "context deadline exceeded"
    }
    return false
  }
}
