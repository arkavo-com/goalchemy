// SPDX-License-Identifier: Apache-2.0
extension GNative {
  static func lib_os_open(_ arguments: [GValue], _ types: [Int] = []) -> GFrame {
    frame("lib.os.open", arguments, types)
  }
}
