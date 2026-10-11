// SPDX-License-Identifier: Apache-2.0
extension GNative {
  static func lib_http_open(_ arguments: [GValue], _ types: [Int] = []) -> GFrame {
    frame("lib.http.open", arguments, types)
  }
}
