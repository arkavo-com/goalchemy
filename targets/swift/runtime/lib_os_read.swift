// SPDX-License-Identifier: Apache-2.0
extension GNative {
  static func lib_os_read(_ arguments: [GValue], _ types: [Int] = []) -> GFrame {
    frame("lib.os.read", arguments, types)
  }
}
