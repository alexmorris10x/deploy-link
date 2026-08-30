import Foundation
import Network

struct RemotePairingReport {
    let wireProtocolVersion: Int
}

enum RemotePairingProbeError: LocalizedError {
    case couldNotCreateRequest
    case connection(message: String, domain: String, code: Int)
    case timedOut
    case closedEarly
    case invalidHeader
    case invalidResponse
    case incomingTunnelsDisabled

    var errorDescription: String? {
        switch self {
        case .couldNotCreateRequest: return "The RemotePairing request could not be encoded."
        case .connection(let message, _, _): return "Connection error: \(message)"
        case .timedOut: return "The RemotePairing endpoint timed out."
        case .closedEarly: return "The RemotePairing endpoint closed early."
        case .invalidHeader: return "The endpoint did not return a RemotePairing header."
        case .invalidResponse: return "The endpoint returned an invalid RemotePairing response."
        case .incomingTunnelsDisabled: return "RemotePairing answered but did not allow incoming tunnels."
        }
    }
}

enum RemotePairingProbe {
    static func run(
        runID: String,
        completion: @escaping (Result<RemotePairingReport, Error>) -> Void
    ) {
        do {
            let request = try requestFrame()
            RemotePairingProbeSession(runID: runID, request: request, completion: completion).start()
        } catch {
            ProbeDiagnostics.shared.record(
                "pairing.finished",
                result: "failed",
                runID: runID,
                context: ["reason": "request_encoding"]
            )
            completion(.failure(error))
        }
    }

    private static func requestFrame() throws -> Data {
        let request: [String: Any] = [
            "message": [
                "plain": [
                    "_0": [
                        "request": [
                            "_0": [
                                "handshake": [
                                    "_0": [
                                        "hostOptions": ["attemptPairVerify": true],
                                        "wireProtocolVersion": 19,
                                    ],
                                ],
                            ],
                        ],
                    ],
                ],
            ],
            "originatedBy": "host",
            "sequenceNumber": 0,
        ]

        let body = try JSONSerialization.data(withJSONObject: request, options: [])
        guard body.count <= Int(UInt16.max) else {
            throw RemotePairingProbeError.couldNotCreateRequest
        }

        var frame = Data("RPPairing".utf8)
        var bodyLength = UInt16(body.count).bigEndian
        withUnsafeBytes(of: &bodyLength) { frame.append(contentsOf: $0) }
        frame.append(body)
        return frame
    }
}

private final class RemotePairingProbeSession {
    private let queue = DispatchQueue(label: "com.10x.xcodecellularprobe.rppairing")
    private let connection: NWConnection
    private let runID: String
    private let request: Data
    private let completion: (Result<RemotePairingReport, Error>) -> Void
    private var timeout: DispatchWorkItem?
    private var didFinish = false
    private var stage = "connecting"
    private var lastNetworkError: NSError?

    init(
        runID: String,
        request: Data,
        completion: @escaping (Result<RemotePairingReport, Error>) -> Void
    ) {
        self.runID = runID
        self.request = request
        self.completion = completion
        connection = NWConnection(host: "10.7.0.1", port: 49152, using: .tcp)
    }

    func start() {
        ProbeDiagnostics.shared.record(
            "pairing.connect",
            result: "started",
            runID: runID,
            context: ["port": "49152"]
        )
        connection.stateUpdateHandler = { [self] state in
            switch state {
            case .ready:
                stage = "tcp_ready"
                ProbeDiagnostics.shared.record("pairing.tcp", result: "ready", runID: runID)
                sendRequest()
            case .waiting(let error):
                stage = "tcp_waiting"
                lastNetworkError = error as NSError
                ProbeDiagnostics.shared.record(
                    "pairing.tcp",
                    result: "waiting",
                    runID: runID,
                    context: ProbeDiagnostics.safeErrorContext(error)
                )
            case .failed(let error):
                finish(.failure(connectionError(error)))
            case .cancelled:
                if !didFinish {
                    finish(.failure(RemotePairingProbeError.closedEarly))
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
            finish(.failure(RemotePairingProbeError.timedOut), context: context)
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
            ProbeDiagnostics.shared.record("pairing.request", result: "sent", runID: runID)
            readExactly(11) { [self] result in
                switch result {
                case .success(let header):
                    parseHeader(header)
                case .failure(let error):
                    finish(.failure(error))
                }
            }
        })
    }

    private func parseHeader(_ header: Data) {
        stage = "validating_header"
        guard header.prefix(9) == Data("RPPairing".utf8) else {
            finish(.failure(RemotePairingProbeError.invalidHeader))
            return
        }

        let bodyLength = header.suffix(2).reduce(UInt16(0)) { partial, byte in
            (partial << 8) | UInt16(byte)
        }
        guard bodyLength > 0 else {
            finish(.failure(RemotePairingProbeError.invalidResponse))
            return
        }

        stage = "waiting_body"
        ProbeDiagnostics.shared.record(
            "pairing.header",
            result: "received",
            runID: runID,
            context: ["body_length": String(bodyLength)]
        )
        readExactly(Int(bodyLength)) { [self] result in
            switch result {
            case .success(let body):
                parseBody(body)
            case .failure(let error):
                finish(.failure(error))
            }
        }
    }

    private func parseBody(_ body: Data) {
        stage = "parsing_body"
        ProbeDiagnostics.shared.record(
            "pairing.body",
            result: "received",
            runID: runID,
            context: ["body_length": String(body.count)]
        )
        guard
            let json = try? JSONSerialization.jsonObject(with: body),
            let wireProtocolVersion = findInteger(named: "wireProtocolVersion", in: json),
            let allowsIncomingTunnels = findBoolean(named: "allowsIncomingTunnelConnections", in: json)
        else {
            finish(.failure(RemotePairingProbeError.invalidResponse))
            return
        }

        guard allowsIncomingTunnels else {
            finish(.failure(RemotePairingProbeError.incomingTunnelsDisabled))
            return
        }

        finish(.success(RemotePairingReport(wireProtocolVersion: wireProtocolVersion)))
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
            if let data {
                next.append(data)
            }

            if next.count == count {
                completion(.success(next))
            } else if isComplete || data?.isEmpty != false {
                completion(.failure(RemotePairingProbeError.closedEarly))
            } else {
                readExactly(count, accumulated: next, completion: completion)
            }
        }
    }

    private func findInteger(named name: String, in value: Any) -> Int? {
        if let dictionary = value as? [String: Any] {
            if let number = dictionary[name] as? NSNumber {
                return number.intValue
            }
            for nested in dictionary.values {
                if let match = findInteger(named: name, in: nested) {
                    return match
                }
            }
        } else if let array = value as? [Any] {
            for nested in array {
                if let match = findInteger(named: name, in: nested) {
                    return match
                }
            }
        }
        return nil
    }

    private func findBoolean(named name: String, in value: Any) -> Bool? {
        if let dictionary = value as? [String: Any] {
            if let number = dictionary[name] as? NSNumber {
                return number.boolValue
            }
            for nested in dictionary.values {
                if let match = findBoolean(named: name, in: nested) {
                    return match
                }
            }
        } else if let array = value as? [Any] {
            for nested in array {
                if let match = findBoolean(named: name, in: nested) {
                    return match
                }
            }
        }
        return nil
    }

    private func connectionError(_ error: NWError) -> RemotePairingProbeError {
        let error = error as NSError
        return .connection(
            message: error.localizedDescription,
            domain: error.domain,
            code: error.code
        )
    }

    private func finish(
        _ result: Result<RemotePairingReport, Error>,
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
            context["protocol_version"] = String(report.wireProtocolVersion)
            ProbeDiagnostics.shared.record(
                "pairing.finished",
                result: "succeeded",
                runID: runID,
                context: context
            )
        case .failure(let error):
            context["reason"] = context["reason"] ?? diagnosticReason(for: error)
            if
                let probeError = error as? RemotePairingProbeError,
                case let .connection(_, domain, code) = probeError
            {
                context["network_error_domain"] = domain
                context["network_error_code"] = String(code)
            }
            ProbeDiagnostics.shared.record(
                "pairing.finished",
                result: "failed",
                runID: runID,
                context: context
            )
        }
        completion(result)
    }

    private func diagnosticReason(for error: Error) -> String {
        guard let error = error as? RemotePairingProbeError else { return "unknown" }
        switch error {
        case .couldNotCreateRequest: return "request_encoding"
        case .connection: return "connection"
        case .timedOut: return "timed_out"
        case .closedEarly: return "closed_early"
        case .invalidHeader: return "invalid_header"
        case .invalidResponse: return "invalid_response"
        case .incomingTunnelsDisabled: return "incoming_tunnels_disabled"
        }
    }
}

enum InternetProbe {
    static func run(runID: String, completion: @escaping (Result<Void, Error>) -> Void) {
        ProbeDiagnostics.shared.record("internet.check", result: "started", runID: runID)
        var request = URLRequest(url: URL(string: "https://captive.apple.com/hotspot-detect.html")!)
        request.timeoutInterval = 6
        URLSession.shared.dataTask(with: request) { _, response, error in
            if let error {
                ProbeDiagnostics.shared.record(
                    "internet.check",
                    result: "failed",
                    runID: runID,
                    context: ProbeDiagnostics.safeErrorContext(error)
                )
                completion(.failure(error))
                return
            }
            guard let response = response as? HTTPURLResponse, 200..<400 ~= response.statusCode else {
                let error = URLError(.badServerResponse)
                ProbeDiagnostics.shared.record(
                    "internet.check",
                    result: "failed",
                    runID: runID,
                    context: ProbeDiagnostics.safeErrorContext(error)
                )
                completion(.failure(error))
                return
            }
            ProbeDiagnostics.shared.record(
                "internet.check",
                result: "succeeded",
                runID: runID,
                context: ["http_status": String(response.statusCode)]
            )
            completion(.success(()))
        }.resume()
    }
}
