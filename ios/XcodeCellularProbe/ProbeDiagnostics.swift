import Foundation
import os

final class ProbeDiagnostics {
    static let shared = ProbeDiagnostics()

    private struct Event: Codable {
        let timestamp: String
        let elapsedMilliseconds: Int
        let process: String
        let event: String
        let result: String
        let context: [String: String]
    }

    private struct Report: Codable {
        let schemaVersion: Int
        let runID: String
        let appVersion: String
        let build: String
        let osVersion: String
        let startedAt: String
        var completedAt: String?
        var events: [Event]
    }

    private let queue = DispatchQueue(label: "com.10x.xcodecellularprobe.diagnostics")
    private let logger = Logger(subsystem: "com.10x.xcodecellularprobe", category: "probe")
    private let timestampFormatter = ISO8601DateFormatter()
    private var report: Report?
    private var startUptime: TimeInterval = 0

    private init() {
        timestampFormatter.formatOptions = [.withInternetDateTime, .withFractionalSeconds]
    }

    func start(context: [String: String]) -> String {
        queue.sync {
            let runID = UUID().uuidString
            let startedAt = timestampFormatter.string(from: Date())
            startUptime = ProcessInfo.processInfo.systemUptime
            report = Report(
                schemaVersion: 1,
                runID: runID,
                appVersion: Bundle.main.object(forInfoDictionaryKey: "CFBundleShortVersionString") as? String ?? "unknown",
                build: Bundle.main.object(forInfoDictionaryKey: "CFBundleVersion") as? String ?? "unknown",
                osVersion: ProcessInfo.processInfo.operatingSystemVersionString,
                startedAt: startedAt,
                completedAt: nil,
                events: []
            )
            append(event: "proof.requested", result: "started", runID: runID, context: context)
            return runID
        }
    }

    @discardableResult
    func record(
        _ event: String,
        result: String,
        runID: String,
        context: [String: String] = [:]
    ) -> Bool {
        queue.sync {
            append(event: event, result: result, runID: runID, context: context)
        }
    }

    @discardableResult
    func complete(runID: String, result: String) -> Bool {
        queue.sync {
            guard report?.runID == runID else { return false }
            let didPersistEvent = append(
                event: "proof.finished",
                result: result,
                runID: runID,
                context: [:]
            )
            report?.completedAt = timestampFormatter.string(from: Date())
            return persist() && didPersistEvent
        }
    }

    static func safeErrorContext(_ error: Error) -> [String: String] {
        let error = error as NSError
        return [
            "error_domain": error.domain,
            "error_code": String(error.code),
        ]
    }

    private func append(
        event: String,
        result: String,
        runID: String,
        context: [String: String]
    ) -> Bool {
        guard var report, report.runID == runID else { return false }
        report.events.append(Event(
            timestamp: timestampFormatter.string(from: Date()),
            elapsedMilliseconds: Int((ProcessInfo.processInfo.systemUptime - startUptime) * 1_000),
            process: "app",
            event: event,
            result: result,
            context: context
        ))
        self.report = report
        logger.info("event=\(event, privacy: .public) result=\(result, privacy: .public)")
        return persist()
    }

    private func persist() -> Bool {
        guard let report else { return false }
        do {
            let encoder = JSONEncoder()
            encoder.outputFormatting = [.prettyPrinted, .sortedKeys]
            let data = try encoder.encode(report)
            let documents = try FileManager.default.url(
                for: .documentDirectory,
                in: .userDomainMask,
                appropriateFor: nil,
                create: true
            )
            try data.write(
                to: documents.appendingPathComponent("latest-probe-diagnostics.json"),
                options: .atomic
            )
            return true
        } catch {
            let error = error as NSError
            logger.error("event=diagnostic.persist result=failed error_domain=\(error.domain, privacy: .public) error_code=\(error.code, privacy: .public)")
            return false
        }
    }
}
