// SPDX-License-Identifier: Apache-2.0
extension GNative {
  static func lib_os_close(_ arguments: [GValue], _ types: [Int] = []) -> GFrame {
    frame("lib.os.close", arguments, types)
  }
}
