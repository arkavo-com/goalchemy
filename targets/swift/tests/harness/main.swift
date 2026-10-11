// SPDX-License-Identifier: Apache-2.0
import Foundation

#if os(Linux)
  import Glibc
#else
  import Darwin
#endif

gHarnessInitialize()
while let line = readLine() {
  if line.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty { continue }
  var response: [String: Any] = ["v": 1]
  do {
    let request = try JSONSerialization.jsonObject(with: Data(line.utf8)) as! [String: Any]
    response["id"] = request["id"]
    guard request["v"] as? Int == 1 else { throw GFault("unsupported protocol version") }
    guard let name = request["case"] as? String, let spec = gHarnessCases[name] else {
      throw GFault("unknown case")
    }
    let raw = request["let"] as? [String: Any] ?? [:]
    var values: [String: GValue] = [:]
    for binding in spec.bindings {
      if binding.kind == "ref" {
        values[binding.name] = values[binding.of]
      } else if binding.kind == "view" {
        let obj = GCodec.object(raw[binding.name]!)
        let base = values[binding.of]!
        values[binding.name] = try GSlice.reslice(
          base, obj["lo"].map { .int(GCodec.integer($0)) },
          obj["hi"].map { .int(GCodec.integer($0)) }, obj["max"].map { .int(GCodec.integer($0)) })
      } else {
        values[binding.name] = try GCodec.decode(raw[binding.name]!, binding.type)
      }
    }
    let args = spec.arguments.map { values[$0]! }
    let owner = GOwner(
      host: spec.contract.hasPrefix("lib.http.") || spec.contract == "lib.callback.request"
        || spec.contract.hasPrefix("lib.crypto."))
    owner.harness = true
    let result = try owner.run(GCodec.execute(spec, args))
    response["status"] = "returned"
    response["results"] = try spec.outputs.enumerated().map {
      try GCodec.encode(result[$0.offset], $0.element)
    }
    var after: [String: Any] = [:]
    for binding in spec.bindings {
      after[binding.name] = try GCodec.encode(values[binding.name]!, binding.type)
    }
    response["after"] = after
  } catch let p as GPanic {
    response["status"] = "panic"
    response["panic"] = String(decoding: GFormat(p.value), as: UTF8.self)
  } catch let f as GFatal {
    response["status"] = f.message.contains("all goroutines") ? "blocked" : "harness_failure"
    response["error"] = f.message
  } catch {
    response["status"] = "harness_failure"
    response["error"] = String(describing: error)
  }
  if let data = try? JSONSerialization.data(withJSONObject: response, options: [.sortedKeys]),
    let text = String(data: data, encoding: .utf8)
  {
    print(text)
    fflush(stdout)
  }
}
