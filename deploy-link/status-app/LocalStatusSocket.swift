import Foundation
import Darwin

/// Read-only control request using the daemon's existing owner-only Unix socket.
enum LocalStatusSocket {
    static func read(path: String, timeout: TimeInterval = 1) -> Data? {
        var info = stat()
        guard lstat(path, &info) == 0, (info.st_mode & S_IFMT) == S_IFSOCK,
              info.st_uid == getuid(), info.st_mode & 0o077 == 0 else { return nil }
        let fd = socket(AF_UNIX, SOCK_STREAM, 0)
        guard fd >= 0 else { return nil }; defer { close(fd) }
        _ = fcntl(fd, F_SETFL, O_NONBLOCK)
        var noSignal: Int32 = 1
        setsockopt(fd, SOL_SOCKET, SO_NOSIGPIPE, &noSignal, socklen_t(MemoryLayout<Int32>.size))
        var address = sockaddr_un(); address.sun_family = sa_family_t(AF_UNIX)
        let bytes = Array(path.utf8) + [0]
        guard bytes.count <= MemoryLayout.size(ofValue: address.sun_path) else { return nil }
        withUnsafeMutableBytes(of: &address.sun_path) { $0.copyBytes(from: bytes) }
        address.sun_len = UInt8(MemoryLayout<sockaddr_un>.size)
        let connected = withUnsafePointer(to: &address) { pointer in
            pointer.withMemoryRebound(to: sockaddr.self, capacity: 1) { Darwin.connect(fd, $0, socklen_t(MemoryLayout<sockaddr_un>.size)) }
        }
        guard connected == 0 || errno == EINPROGRESS else { return nil }
        let end = ProcessInfo.processInfo.systemUptime + timeout
        func ready(_ events: Int16) -> Bool {
            let ms = max(0, end - ProcessInfo.processInfo.systemUptime) * 1000
            guard ms > 0 else { return false }
            var descriptor = pollfd(fd: fd, events: events, revents: 0)
            return poll(&descriptor, 1, Int32(ms)) > 0 && descriptor.revents & (events | Int16(POLLHUP)) != 0
        }
        guard ready(Int16(POLLOUT)) else { return nil }
        let request = Array("{\"command\":\"status\"}\n".utf8)
        var sent = 0
        while sent < request.count {
            let count = request.withUnsafeBytes { Darwin.send(fd, $0.baseAddress!.advanced(by: sent), request.count - sent, 0) }
            if count <= 0 { return nil }; sent += count
        }
        var data = Data(), buffer = [UInt8](repeating: 0, count: 4096)
        while data.count < 65536, ready(Int16(POLLIN)) {
            let count = Darwin.read(fd, &buffer, buffer.count)
            guard count > 0 else { break }
            data.append(contentsOf: buffer.prefix(count))
            if data.contains(10) { return data }
        }
        return nil
    }
}
