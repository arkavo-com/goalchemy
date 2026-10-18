// SPDX-License-Identifier: Apache-2.0
extension GNative {
  static func lib_os_create_temp(_ arguments: [GValue], _ types: [Int] = []) -> GFrame {
    frame("lib.os.create_temp", arguments, types)
  }
}
