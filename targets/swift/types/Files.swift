// SPDX-License-Identifier: Apache-2.0
import Foundation

enum GFiles {
  static let maxFile = 1 << 30
  static func status(_ e: Int32) -> Int64 {
    switch e {
    case ENOENT: return 1
    case EEXIST: return 2
    case EACCES, EPERM: return 3
    case EISDIR: return 4
    case ENOTDIR: return 5
    case EFBIG: return 6
    case ENOSYS, EOPNOTSUPP: return 7
    case EINVAL: return 8
    default: return 9
    }
  }
  static func path(_ name: GValue) -> ([CChar]?, Int64) {
    let b = name.bytes
    if b.isEmpty { return (nil, 1) }
    if b.contains(0) { return (nil, 8) }
    return (b.map { CChar(bitPattern: $0) } + [0], 0)
  }
  static func readFile(_ name: GValue) -> [GValue] {
    let (p, bad) = path(name)
    guard let p = p else { return [GNative.nilBytes(), .int(bad)] }
    let fd = open(p, O_RDONLY | O_CLOEXEC)
    if fd < 0 { return [GNative.nilBytes(), .int(status(errno))] }
    defer { close(fd) }
    var st = stat()
    if fstat(fd, &st) == 0 && Int(st.st_size) > maxFile { return [GNative.nilBytes(), .int(6)] }
    var data: [UInt8] = []
    var chunk = [UInt8](repeating: 0, count: 1 << 16)
    while true {
      let r = chunk.withUnsafeMutableBytes { read(fd, $0.baseAddress, $0.count) }
      if r < 0 {
        if errno == EINTR { continue }
        return [GNative.nilBytes(), .int(status(errno))]
      }
      if r == 0 { break }
      data.append(contentsOf: chunk[0..<r])
      if data.count > maxFile { return [GNative.nilBytes(), .int(6)] }
    }
    return [GNative.bytes(data), .int(0)]
  }
  static func writeFile(_ name: GValue, _ data: GValue, _ perm: GValue) throws -> [GValue] {
    let (p, bad) = path(name)
    guard let p = p else { return [.int(bad)] }
    let body = try GNative.buffer(data)
    if body.count > maxFile { return [.int(6)] }
    let fd = open(p, O_WRONLY | O_CREAT | O_TRUNC | O_CLOEXEC, mode_t(perm.intValue & 0o777))
    if fd < 0 { return [.int(status(errno))] }
    var code: Int64 = 0
    var done = 0
    while done < body.count {
      let w = body.withUnsafeBytes { write(fd, $0.baseAddress! + done, body.count - done) }
      if w < 0 {
        if errno == EINTR { continue }
        code = status(errno)
        break
      }
      done += w
    }
    if close(fd) != 0 && code == 0 { code = status(errno) }
    return [.int(code)]
  }

  // Open files by handle; handles are never reused.
  static let maxChunk = 1 << 20
  static var files: [Int64: Int32] = [:]
  static var last: Int64 = 0
  static func register(_ fd: Int32) -> Int64 {
    last += 1
    files[last] = fd
    return last
  }
  static func fd(_ handle: GValue) -> Int32? { files[handle.intValue] }
  static func chunk(_ r: Int, _ data: [UInt8]) -> [GValue] {
    if r < 0 { return [GNative.nilBytes(), .bool(false), .int(status(errno))] }
    return [GNative.bytes(Array(data[0..<r])), .bool(r == 0), .int(0)]
  }
  static func openHandle(_ name: GValue, _ flag: GValue, _ perm: GValue) -> [GValue] {
    let fl = flag.intValue
    if fl & ~63 != 0 || fl & 3 == 3 { return [.int(0), .int(8)] }
    let (p, bad) = path(name)
    guard let p = p else { return [.int(0), .int(bad)] }
    var oflag: Int32 = fl & 3 == 2 ? O_RDWR : fl & 3 == 1 ? O_WRONLY : O_RDONLY
    if fl & 4 != 0 { oflag |= O_APPEND }
    if fl & 8 != 0 { oflag |= O_CREAT }
    if fl & 16 != 0 { oflag |= O_EXCL }
    if fl & 32 != 0 { oflag |= O_TRUNC }
    let fd = open(p, oflag | O_CLOEXEC, mode_t(perm.intValue & 0o777))
    if fd < 0 { return [.int(0), .int(status(errno))] }
    return [.int(register(fd)), .int(0)]
  }
  static func readHandle(_ file: GValue, _ max: GValue) -> [GValue] {
    let n = Int(max.intValue)
    guard let fd = fd(file), n >= 1, n <= maxChunk else { return [GNative.nilBytes(), .bool(false), .int(8)] }
    var data = [UInt8](repeating: 0, count: n)
    var r: Int
    repeat { r = data.withUnsafeMutableBytes { read(fd, $0.baseAddress, n) } } while r < 0 && errno == EINTR
    return chunk(r, data)
  }
  static func readAtHandle(_ file: GValue, _ offset: GValue, _ max: GValue) -> [GValue] {
    let n = Int(max.intValue)
    let off = offset.intValue
    guard let fd = fd(file), n >= 1, n <= maxChunk, off >= 0 else { return [GNative.nilBytes(), .bool(false), .int(8)] }
    var data = [UInt8](repeating: 0, count: n)
    var r: Int
    repeat { r = data.withUnsafeMutableBytes { pread(fd, $0.baseAddress, n, off_t(off)) } } while r < 0 && errno == EINTR
    return chunk(r, data)
  }
  static func writeHandle(_ file: GValue, _ data: GValue) throws -> [GValue] {
    guard let fd = fd(file) else { return [.int(0), .int(8)] }
    let body = try GNative.buffer(data)
    if body.count > maxChunk { return [.int(0), .int(8)] }
    var done = 0
    var code: Int64 = 0
    while done < body.count {
      let w = body.withUnsafeBytes { write(fd, $0.baseAddress! + done, body.count - done) }
      if w < 0 {
        if errno == EINTR { continue }
        code = status(errno)
        break
      }
      if w == 0 { code = 9; break }
      done += w
    }
    return [.int(Int64(done)), .int(code)]
  }
  static func seekHandle(_ file: GValue, _ offset: GValue, _ whence: GValue) -> [GValue] {
    let w = whence.intValue
    guard let fd = fd(file), w >= 0, w <= 2 else { return [.int(0), .int(8)] }
    let pos = lseek(fd, off_t(offset.intValue), w == 0 ? SEEK_SET : w == 1 ? SEEK_CUR : SEEK_END)
    if pos < 0 { return [.int(0), .int(status(errno))] }
    return [.int(Int64(pos)), .int(0)]
  }
  static func syncHandle(_ file: GValue) -> [GValue] {
    guard let fd = fd(file) else { return [.int(8)] }
    return [.int(fsync(fd) == 0 ? 0 : status(errno))]
  }
  static func closeHandle(_ file: GValue) -> [GValue] {
    guard let fd = files.removeValue(forKey: file.intValue) else { return [.int(8)] }
    return [.int(close(fd) == 0 ? 0 : status(errno))]
  }
  static func statPath(_ name: GValue) -> [GValue] {
    let (p, bad) = path(name)
    guard let p = p else { return [.int(0), .int(0), .bool(false), .int(bad)] }
    var st = stat()
    if stat(p, &st) != 0 { return [.int(0), .int(0), .bool(false), .int(status(errno))] }
    return [.int(Int64(st.st_size)), .int(Int64(st.st_mode & 0o777)), .bool((st.st_mode & S_IFMT) == S_IFDIR), .int(0)]
  }
  static func createTemp(_ dir: GValue, _ prefix: GValue, _ suffix: GValue) -> [GValue] {
    let (d, pre, suf) = (dir.bytes, prefix.bytes, suffix.bytes)
    if d.contains(0) || pre.contains(0) || suf.contains(0) || pre.contains(47) || suf.contains(47) { return [.int(0), .string([]), .int(8)] }
    var base = d
    if base.isEmpty {
      let env = getenv("TMPDIR")
      let text = env != nil && env![0] != 0 ? String(cString: env!) : "/tmp"
      base = Array(text.utf8)
    }
    let alphabet = Array("0123456789abcdefghijklmnopqrstuvwxyz".utf8)
    var generator = SystemRandomNumberGenerator()
    for attempt in 0... {
      var name = base + [47] + pre
      for _ in 0..<10 { name.append(alphabet[Int(generator.next() % 36)]) }
      name += suf
      let fd = open(name.map { CChar(bitPattern: $0) } + [0], O_RDWR | O_CREAT | O_EXCL | O_CLOEXEC, 0o600)
      if fd >= 0 { return [.int(register(fd)), .string(name), .int(0)] }
      if errno == EEXIST && attempt < 10000 { continue }
      return [.int(0), .string(name), .int(status(errno))]
    }
    return [.int(0), .string([]), .int(9)]
  }
  static func removePath(_ name: GValue) -> [GValue] {
    let (p, bad) = path(name)
    guard let p = p else { return [.int(bad)] }
    if unlink(p) == 0 { return [.int(0)] }
    var e = errno
    if rmdir(p) == 0 { return [.int(0)] }
    if errno != ENOTDIR { e = errno }
    return [.int(status(e))]
  }
  static func renamePath(_ oldpath: GValue, _ newpath: GValue) -> [GValue] {
    let (from, bad) = path(oldpath)
    guard let from = from else { return [.int(bad)] }
    let (to, bad2) = path(newpath)
    guard let to = to else { return [.int(bad2)] }
    return [.int(rename(from, to) == 0 ? 0 : status(errno))]
  }
}
