import Foundation

/// Terminal receipts are immutable. Re-read active/latest receipts, rescan on directory
/// changes and at least once per minute; manual Refresh always reconciles the full history.
final class ReceiptCache<Value: Decodable> {
    private struct Entry {
        var directoryDate: Date?
        var scannedAt: TimeInterval
        var values: [URL: (value: Value, date: Date)]
    }
    private var entries: [URL: Entry] = [:]
    private let updated: (Value) -> Date
    private let active: (Value) -> Bool
    private let decoder = JSONDecoder()
    private(set) var decodedCount = 0
    init(updated: @escaping (Value) -> Date, active: @escaping (Value) -> Bool) { self.updated = updated; self.active = active }
    func latest(in root: URL, force: Bool = false) -> Value? {
        let now = ProcessInfo.processInfo.systemUptime
        let date = (try? FileManager.default.attributesOfItem(atPath: root.path))?[.modificationDate] as? Date
        var entry = entries[root] ?? Entry(directoryDate: nil, scannedAt: -.infinity, values: [:])
        let urls: [URL]
        if force || entry.scannedAt + 60 < now || entry.directoryDate != date {
            urls = ((try? FileManager.default.contentsOfDirectory(at: root, includingPropertiesForKeys: nil, options: [.skipsHiddenFiles])) ?? []).map { $0.appendingPathComponent("receipt.json") }
            entry.values = [:]; entry.directoryDate = date; entry.scannedAt = now
        } else {
            let latestURL = entry.values.max { $0.value.date < $1.value.date }?.key
            urls = entry.values.filter { active($0.value.value) || $0.key == latestURL }.map(\.key)
        }
        for url in urls {
            if let data = try? Data(contentsOf: url), let value = try? decoder.decode(Value.self, from: data) {
                entry.values[url] = (value, updated(value)); decodedCount += 1
            } else { entry.values.removeValue(forKey: url) }
        }
        entries[root] = entry
        return entry.values.values.max { $0.date < $1.date }?.value
    }
}
