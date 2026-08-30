import Foundation
import Network

struct LockdownReport {
    let serviceType: String
}

enum LockdownProbeError: LocalizedError {
    case couldNotCreateRequest
    case connection(message: String, domain: String, code: Int)
    case timedOut
    case closedEarly
    case invalidLength
    case invalidResponse
    case unexpectedServiceType(String)

    var errorDescription: String? {
        switch self {
        case .couldNotCreateRequest: return "The lockdownd request could not be encoded."
        case .connection(let message, _, _): return "Connection error: \(message)"
        case .timedOut: return "The lockdownd endpoint timed out."
        case .closedEarly: return "The lockdownd endpoint closed early."
        case .invalidLength: return "The endpoint returned an invalid lockdownd frame length."
        case .invalidResponse: return "The endpoint did not return a valid lockdownd plist."
        case .unexpectedServiceType(let type): return "The endpoint returned an unexpected service type: \(type)."
        }
    }
}

enum LockdownProbe {
    static func run(
        runID: String,
        completion: @escaping (Result<LockdownReport, Error>) -> Void
    ) {
        do {
            let request = try requestFrame()
            LockdownProbeSession(runID: runID, request: request, completion: completion).start()
        } catch {
            ProbeDiagnostics.shared.record(
                "lockdown.finished",
                result: "failed",
                runID: runID,
                context: ["reason": "request_encoding"]
            )
            completion(.failure(error))
        }
    }

    private static func requestFrame() throws -> Data {
        let request: [String: Any] = [
            "Label": "Xcode Cellular Probe",
            "Request": "QueryType",
        ]
        let body = try PropertyListSerialization.data(
            fromPropertyList: request,
            format: .xml,
            options: 0
        )
        guard !body.isEmpty, body.count <= 1_048_576 else {
            throw LockdownProbeError.couldNotCreateRequest
        }

        var frame = Data()
        var bodyLength = UInt32(body.count).bigEndian
        withUnsafeBytes(of: &bodyLength) { frame.append(contentsOf: $0) }
        frame.append(body)
        return frame
    }
}

private final class LockdownProbeSession {
    private let queue = DispatchQueue(label: "com.10x.xcodecellularprobe.lockdown")
    private let connection = NWConnection(host: "10.7.0.1", port: 62078, using: .tcp)
    private let runID: String
    private let request: Data
    private let completion: (Result<LockdownReport, Error>) -> Void
    private var timeout: DispatchWorkItem?
    private var didFinish = false
    private var stage = "connecting"
    private var lastNetworkError: NSError?

    init(
        runID: String,
        request: Data,
        completion: @escaping (Result<LockdownReport, Error>) -> Void
    ) {
        self.runID = runID
        self.request = request
        self.completion = completion
    }

    func start() {
        ProbeDiagnostics.shared.record(
            "lockdown.connect",
            result: "started",
            runID: runID,
            context: ["port": "62078"]
        )
        connection.stateUpdateHandler = { [self] state in
            switch state {
            case .ready:
                stage = "tcp_ready"
                ProbeDiagnostics.shared.record("lockdown.tcp", result: "ready", runID: runID)
                sendRequest()
            case .waiting(let error):
                stage = "tcp_waiting"
                lastNetworkError = error as NSError
                ProbeDiagnostics.shared.record(
                    "lockdown.tcp",
                    result: "waiting",
                    runID: runID,
                    context: ProbeDiagnostics.safeErrorContext(error)
                )
            case .failed(let error):
                finish(.failure(connectionError(error)))
            case .cancelled:
                if !didFinish {
                    finish(.failure(LockdownProbeError.closedEarly))
                }
            default:
                break
            }
        }

        let timeout = DispatchWorkItem { [self] in
            var context = ["reason": "timed_out", "stage": stage]
            if let lastNetworkError {
                context.merge([
                    "network_error_domain": lastNetworkError.domain,
                    "network_error_code": String(lastNetworkError.code),
                ]) { _, newValue in newValue }
            }
            finish(.failure(LockdownProbeError.timedOut), context: context)
        }
        self.timeout = timeout
        queue.asyncAfter(deadline: .now() + 6, execute: timeout)
        connection.start(queue: queue)
    }

    private func sendRequest() {
        stage = "sending_request"
        connection.send(content: request, completion: .contentProcessed { [self] error in
            if let error {
                finish(.failure(connectionError(error)))
                return
            }
            stage = "waiting_header"
            ProbeDiagnostics.shared.record("lockdown.request", result: "sent", runID: runID)
            readExactly(4) { [self] result in
                switch result {
                case .success(let header): parseHeader(header)
                case .failure(let error): finish(.failure(error))
                }
            }
        })
    }

    private func parseHeader(_ header: Data) {
        let bodyLength = header.reduce(UInt32(0)) { partial, byte in
            (partial << 8) | UInt32(byte)
        }
        guard bodyLength > 0, bodyLength <= 1_048_576 else {
            finish(.failure(LockdownProbeError.invalidLength))
            return
        }

        stage = "waiting_body"
        ProbeDiagnostics.shared.record(
            "lockdown.header",
            result: "received",
            runID: runID,
            context: ["body_length": String(bodyLength)]
        )
        readExactly(Int(bodyLength)) { [self] result in
            switch result {
            case .success(let body): parseBody(body)
            case .failure(let error): finish(.failure(error))
            }
        }
    }

    private func parseBody(_ body: Data) {
        stage = "parsing_body"
        guard
            let response = try? PropertyListSerialization.propertyList(from: body, options: [], format: nil),
            let dictionary = response as? [String: Any],
            let serviceType = dictionary["Type"] as? String
        else {
            finish(.failure(LockdownProbeError.invalidResponse))
            return
        }

        ProbeDiagnostics.shared.record(
            "lockdown.body",
            result: "received",
            runID: runID,
            context: ["body_length": String(body.count)]
        )
        guard serviceType == "com.apple.mobile.lockdown" else {
            finish(.failure(LockdownProbeError.unexpectedServiceType(serviceType)))
            return
        }
        finish(.success(LockdownReport(serviceType: serviceType)))
    }

    private func readExactly(
        _ count: Int,
        accumulated: Data = Data(),
        completion: @escaping (Result<Data, Error>) -> Void
    ) {
        let remaining = count - accumulated.count
        guard remaining > 0 else {
            completion(.success(accumulated))
            return
        }

        connection.receive(minimumIncompleteLength: 1, maximumLength: remaining) { [self] data, _, isComplete, error in
            if let error {
                completion(.failure(connectionError(error)))
                return
            }

            var next = accumulated
            if let data { next.append(data) }
            if next.count == count {
                completion(.success(next))
            } else if isComplete || data?.isEmpty != false {
                completion(.failure(LockdownProbeError.closedEarly))
            } else {
                readExactly(count, accumulated: next, completion: completion)
            }
        }
    }

    private func connectionError(_ error: NWError) -> LockdownProbeError {
        let error = error as NSError
        return .connection(
            message: error.localizedDescription,
            domain: error.domain,
            code: error.code
        )
    }

    private func finish(
        _ result: Result<LockdownReport, Error>,
        context suppliedContext: [String: String] = [:]
    ) {
        guard !didFinish else { return }
        didFinish = true
        timeout?.cancel()
        connection.stateUpdateHandler = nil
        connection.cancel()

        var context = suppliedContext
        context["stage"] = context["stage"] ?? stage
        switch result {
        case .success(let report):
            context["service_type"] = report.serviceType
            ProbeDiagnostics.shared.record(
                "lockdown.finished",
                result: "succeeded",
                runID: runID,
                context: context
            )
        case .failure(let error):
            context["reason"] = context["reason"] ?? diagnosticReason(for: error)
            if
                let probeError = error as? LockdownProbeError,
                case let .connection(_, domain, code) = probeError
            {
                context["network_error_domain"] = domain
                context["network_error_code"] = String(code)
            }
            ProbeDiagnostics.shared.record(
                "lockdown.finished",
                result: "failed",
                runID: runID,
                context: context
            )
        }
        completion(result)
    }

    private func diagnosticReason(for error: Error) -> String {
        guard let error = error as? LockdownProbeError else { return "unknown" }
        switch error {
        case .couldNotCreateRequest: return "request_encoding"
        case .connection: return "connection"
        case .timedOut: return "timed_out"
        case .closedEarly: return "closed_early"
        case .invalidLength: return "invalid_length"
        case .invalidResponse: return "invalid_response"
        case .unexpectedServiceType: return "unexpected_service_type"
        }
    }
}
