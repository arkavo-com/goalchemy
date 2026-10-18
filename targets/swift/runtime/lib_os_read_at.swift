// SPDX-License-Identifier: Apache-2.0
extension GNative {
  static func lib_os_read_at(_ arguments: [GValue], _ types: [Int] = []) -> GFrame {
    frame("lib.os.read_at", arguments, types)
  }
}
