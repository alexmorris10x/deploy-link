import Foundation
import Darwin

final class CommandCancellation: @unchecked Sendable {
    private let lock = NSLock()
    private var value = false
    func cancel() { lock.lock(); value = true; lock.unlock() }
    var isCancelled: Bool { lock.lock(); defer { lock.unlock() }; return value }
}

struct CommandResult {
    let data: Data
    let status: Int32
    let truncated: Bool
}

struct CommandFailure: LocalizedError {
    let message: String
    var errorDescription: String? { message }
}

/// Owns only this invocation's process group. launchd-managed warm bridges are not members.
enum BoundedProcess {
    static func run(executable: URL, arguments: [String], timeout: TimeInterval = 30,
                    limit: Int = 256 * 1024, cancellation: CommandCancellation = CommandCancellation()) throws -> CommandResult {
        if cancellation.isCancelled { throw CancellationError() }
        var descriptors: [Int32] = [0, 0]
        guard pipe(&descriptors) == 0 else { throw CommandFailure(message: "Could not open command output pipe") }
        let readFD = descriptors[0], writeFD = descriptors[1]
        defer { close(readFD) }
        var actions: posix_spawn_file_actions_t?
        posix_spawn_file_actions_init(&actions)
        defer { posix_spawn_file_actions_destroy(&actions) }
        posix_spawn_file_actions_addopen(&actions, STDIN_FILENO, "/dev/null", O_RDONLY, 0)
        posix_spawn_file_actions_adddup2(&actions, writeFD, STDOUT_FILENO)
        posix_spawn_file_actions_adddup2(&actions, writeFD, STDERR_FILENO)
        posix_spawn_file_actions_addclose(&actions, readFD)
        posix_spawn_file_actions_addclose(&actions, writeFD)
        var attributes: posix_spawnattr_t?
        posix_spawnattr_init(&attributes)
        defer { posix_spawnattr_destroy(&attributes) }
        posix_spawnattr_setflags(&attributes, Int16(POSIX_SPAWN_SETPGROUP))
        posix_spawnattr_setpgroup(&attributes, 0)
        let strings = ([executable.path] + arguments).map { strdup($0) }
        defer { strings.forEach { free($0) } }
        var argv = strings + [nil]
        var pid: pid_t = 0
        let spawned = posix_spawn(&pid, executable.path, &actions, &attributes, &argv, environ)
        close(writeFD)
        guard spawned == 0 else { throw CommandFailure(message: "Could not start command: \(String(cString: strerror(spawned)))") }
        _ = fcntl(readFD, F_SETFL, O_NONBLOCK)
        let end = ProcessInfo.processInfo.systemUptime + timeout
        var terminateAt: TimeInterval?
        var timedOut = false, cancelled = false, reaped = false, eof = false
        var waitStatus: Int32 = 0
        var data = Data(), truncated = false
        var buffer = [UInt8](repeating: 0, count: 16 * 1024)
        while !reaped || !eof {
            let now = ProcessInfo.processInfo.systemUptime
            if terminateAt == nil && (cancellation.isCancelled || now >= end) {
                cancelled = cancellation.isCancelled; timedOut = !cancelled
                terminateAt = now; kill(-pid, SIGTERM)
            }
            if let start = terminateAt, now - start >= 1 { kill(-pid, SIGKILL) }
            var pollFD = pollfd(fd: readFD, events: Int16(POLLIN | POLLHUP), revents: 0)
            _ = poll(&pollFD, 1, 25)
            while true {
                let count = read(readFD, &buffer, buffer.count)
                if count == 0 { eof = true; break }
                if count < 0 { if errno == EINTR { continue }; break }
                data.append(contentsOf: buffer.prefix(count))
                if data.count > limit { data.removeFirst(data.count - limit); truncated = true }
                // An endlessly noisy child must not prevent the deadline/cancel check.
                if ProcessInfo.processInfo.systemUptime >= end || cancellation.isCancelled { break }
            }
            if !reaped { reaped = waitpid(pid, &waitStatus, WNOHANG) == pid }
            if let start = terminateAt, now - start >= 2, reaped { break }
        }
        if !reaped { kill(-pid, SIGKILL); _ = waitpid(pid, &waitStatus, 0) }
        if cancelled { throw CancellationError() }
        if timedOut { throw CommandFailure(message: "Command timed out. The device bridge remains running; refresh to check any submitted install job.") }
        let exitStatus: Int32 = waitStatus & 0x7f == 0 ? (waitStatus >> 8) & 0xff : 128 + (waitStatus & 0x7f)
        return CommandResult(data: data, status: exitStatus, truncated: truncated)
    }

    static func runAsync(executable: URL, arguments: [String], timeout: TimeInterval = 30) async throws -> CommandResult {
        let cancellation = CommandCancellation()
        return try await withTaskCancellationHandler(operation: {
            try await withCheckedThrowingContinuation { continuation in
                DispatchQueue.global(qos: .userInitiated).async {
                    do { continuation.resume(returning: try run(executable: executable, arguments: arguments, timeout: timeout, cancellation: cancellation)) }
                    catch { continuation.resume(throwing: error) }
                }
            }
        }, onCancel: { cancellation.cancel() })
    }
}
