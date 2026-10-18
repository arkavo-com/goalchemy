// SPDX-License-Identifier: Apache-2.0
extension GNative {
  static func lib_os_rename(_ arguments: [GValue], _ types: [Int] = []) -> GFrame {
    frame("lib.os.rename", arguments, types)
  }
}
