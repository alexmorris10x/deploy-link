import Foundation
import Darwin
func run(_ script: String, timeout: TimeInterval = 3, cancellation: CommandCancellation = CommandCancellation()) throws -> CommandResult {
    try BoundedProcess.run(executable: URL(fileURLWithPath: "/bin/sh"), arguments: ["-c", script], timeout: timeout, cancellation: cancellation)
}
let large = try run("/usr/bin/yes x | /usr/bin/head -c 1048576")
precondition(large.status == 0 && large.data.count == 256 * 1024 && large.truncated)
let failure = try run("echo failure >&2; exit 7")
precondition(failure.status == 7 && String(decoding: failure.data, as: UTF8.self).contains("failure"))
let started = ProcessInfo.processInfo.systemUptime
var timedOut = false
 do { _ = try run("sleep 60 & wait", timeout: 0.15) }
 catch { timedOut = error.localizedDescription.contains("timed out") }
precondition(timedOut && ProcessInfo.processInfo.systemUptime - started < 3)
let cancellation = CommandCancellation()
DispatchQueue.global().asyncAfter(deadline: .now() + 0.1) { cancellation.cancel() }
var cancelled = false
do { _ = try run("sleep 60 & wait", cancellation: cancellation) }
catch is CancellationError { cancelled = true }
precondition(cancelled)
// Exercise the same newline protocol as the Go daemon, including a stalled peer.
func socketFixture(respond: Bool) {
    let path = "/tmp/deploy-status-" + UUID().uuidString + ".sock"
    let server = socket(AF_UNIX, SOCK_STREAM, 0)
    precondition(server >= 0)
    defer { close(server); unlink(path) }
    var address = sockaddr_un(); address.sun_family = sa_family_t(AF_UNIX)
    address.sun_len = UInt8(MemoryLayout<sockaddr_un>.size)
    withUnsafeMutableBytes(of: &address.sun_path) { $0.copyBytes(from: Array(path.utf8) + [0]) }
    let result = withUnsafePointer(to: &address) { pointer in
        pointer.withMemoryRebound(to: sockaddr.self, capacity: 1) { bind(server, $0, socklen_t(MemoryLayout<sockaddr_un>.size)) }
    }
    precondition(result == 0 && chmod(path, 0o600) == 0 && listen(server, 1) == 0)
    let done = DispatchSemaphore(value: 0)
    DispatchQueue.global().async {
        let client = accept(server, nil, nil)
        precondition(client >= 0)
        defer { close(client); done.signal() }
        var request = [UInt8](repeating: 0, count: 256)
        let count = Darwin.read(client, &request, request.count)
        precondition(count > 0 && String(decoding: request.prefix(count), as: UTF8.self) == "{\"command\":\"status\"}\n")
        if respond {
            let response = Array("{\"ok\":true,\"state\":\"active\",\"control_schema\":2,\"final\":true}\n".utf8)
            _ = response.withUnsafeBytes { Darwin.write(client, $0.baseAddress, $0.count) }
        } else { Thread.sleep(forTimeInterval: 0.2) }
    }
    let began = ProcessInfo.processInfo.systemUptime
    let response = LocalStatusSocket.read(path: path, timeout: respond ? 1 : 0.05)
    precondition(respond ? response != nil : response == nil)
    if !respond { precondition(ProcessInfo.processInfo.systemUptime - began < 0.15) }
    precondition(done.wait(timeout: .now() + 2) == .success)
    precondition(chmod(path, 0o666) == 0 && LocalStatusSocket.read(path: path) == nil)
}
socketFixture(respond: true)
socketFixture(respond: false)
struct Receipt: Codable { let stamp: Double; let active: Bool }
let root = FileManager.default.temporaryDirectory.appendingPathComponent("deploy-receipts-" + UUID().uuidString)
try FileManager.default.createDirectory(at: root, withIntermediateDirectories: true)
defer { try? FileManager.default.removeItem(at: root) }
for i in 0..<1000 {
    let directory = root.appendingPathComponent(String(i))
    try FileManager.default.createDirectory(at: directory, withIntermediateDirectories: true)
    try JSONEncoder().encode(Receipt(stamp: Double(i), active: i == 999)).write(to: directory.appendingPathComponent("receipt.json"))
}
let cache = ReceiptCache<Receipt>(updated: { Date(timeIntervalSince1970: $0.stamp) }, active: { $0.active })
precondition(cache.latest(in: root)?.stamp == 999 && cache.decodedCount == 1000)
let before = cache.decodedCount
let tick = ProcessInfo.processInfo.systemUptime
for _ in 0..<10 { precondition(cache.latest(in: root)?.stamp == 999) }
let cached = ProcessInfo.processInfo.systemUptime - tick
precondition(cache.decodedCount - before == 10)
try JSONEncoder().encode(Receipt(stamp: 1001, active: false)).write(to: root.appendingPathComponent("999/receipt.json"), options: .atomic)
precondition(cache.latest(in: root)?.stamp == 1001)
let fullStart = ProcessInfo.processInfo.systemUptime
for _ in 0..<10 { precondition(cache.latest(in: root, force: true)?.stamp == 1001) }
let full = ProcessInfo.processInfo.systemUptime - fullStart
print(String(format: "Passed large-output, failure, timeout/cancel and receipt cache fixtures. 1000 receipts, 10 refreshes: full=%.3fs cached=%.3fs; cached decoded 10 versus 10000.",full,cached))
