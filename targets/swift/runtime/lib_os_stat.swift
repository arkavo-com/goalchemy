// SPDX-License-Identifier: Apache-2.0
extension GNative {
  static func lib_os_stat(_ arguments: [GValue], _ types: [Int] = []) -> GFrame {
    frame("lib.os.stat", arguments, types)
  }
}
