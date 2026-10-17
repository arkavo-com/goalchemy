// SPDX-License-Identifier: Apache-2.0
extension GNative {
  static func lib_task_await(_ arguments: [GValue], _ types: [Int] = []) -> GFrame {
    let frame = GFrame(-1)
    frame.roots = { arguments }
    frame.step = { frame, task in
      if frame.pc == 1 { return .complete(task.rv) }
      frame.pc = 1
      guard case .opaque(let opaque) = arguments[0], let future = opaque as? GFutureState else { return .park }
      future.observed = true
      task.owner.unawaited.removeAll { if case .opaque(let key) = $0.0 { return key === future }; return false }
      if future.done {
        if let panic = GCopyFuturePanic(future.panic) { throw panic }
        return .complete([future.results])
      }
      future.waiters.append(task)
      return .park
    }
    return frame
  }
}
