// SPDX-License-Identifier: Apache-2.0
extension GNative {
  static func lib_http_do_with(_ arguments: [GValue], _ types: [Int] = []) -> GFrame {
    frame("lib.http.do_with", arguments, types)
  }
}
