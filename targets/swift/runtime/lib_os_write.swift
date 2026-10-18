// SPDX-License-Identifier: Apache-2.0
extension GNative {
  static func lib_os_write(_ arguments: [GValue], _ types: [Int] = []) -> GFrame {
    frame("lib.os.write", arguments, types)
  }
}
