// SPDX-License-Identifier: Apache-2.0
final class GFutureState: GOpaque {
  var done = false
  var observed = false
  var results: GValue = .nilValue
  var panic: GPanic?
  var waiters: [GTask] = []
  override func children() -> [GValue] {
    var values = [results]
    var p = panic
    while let current = p { values.append(current.value); p = current.previous }
    return values
  }
  override func releaseEdges() { results = .nilValue; panic = nil; waiters = [] }
}
func GCopyFuturePanic(_ panic: GPanic?) -> GPanic? {
  guard let panic = panic else { return nil }
  let copy = GPanic(panic.value)
  copy.previous = GCopyFuturePanic(panic.previous)
  return copy
}
extension GNative {
  static func lib_task_start(_ arguments: [GValue], _ types: [Int] = []) -> GFrame {
    let frame = GFrame(-1)
    frame.roots = { arguments }
    frame.step = { _, task in
      let future = GFutureState()
      let child = GFrame(-1)
      child.roots = { arguments + [.opaque(future)] }
      child.step = { frame, task in
        if frame.pc == 0 {
          frame.pc = 1
          guard case .interface(let box) = arguments[0] else { throw GFault("invalid future function") }
          return .call(try GFunction.start(box.value, []))
        }
        return .complete(task.rv)
      }
      child.completion = { task, results, panic in
        future.done = true
        future.panic = panic
        if panic == nil { future.results = results[0] }
        if let panic = panic, !future.observed { task.owner.unawaited.append((.opaque(future), panic)) }
        for waiter in future.waiters {
          waiter.resumePanic = GCopyFuturePanic(panic)
          task.owner.ready(waiter, [future.results])
        }
        future.waiters = []
      }
      task.owner.spawn(child)
      return .complete([.opaque(future)])
    }
    return frame
  }
}
