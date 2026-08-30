import Foundation
import Network
import NetworkExtension
import SwiftUI

@MainActor
final class PrototypeModel: ObservableObject {
    @Published private(set) var tunnelStatus: NEVPNStatus = .invalid
    @Published private(set) var networkDescription = "Checking…"
    @Published private(set) var isRunningProof = false
    @Published private(set) var internetResult = "Not run"
    @Published private(set) var pairingResult = "Not run"
    @Published private(set) var lockdownResult = "Not run"
    @Published private(set) var diagnosticResult = "Not run"
    @Published private(set) var resultSummary = "Enable the local reflector, then run the proof."

    private let providerBundleIdentifier = "com.10x.xcodecellularprobe.Tunnel"
    private var manager: NETunnelProviderManager?
    private var statusObserver: NSObjectProtocol?
    private let pathMonitor = NWPathMonitor()
    private let monitorQueue = DispatchQueue(label: "com.10x.xcodecellularprobe.path")

    init() {
        statusObserver = NotificationCenter.default.addObserver(
            forName: .NEVPNStatusDidChange,
            object: nil,
            queue: .main
        ) { [weak self] _ in
            Task { @MainActor in
                self?.refreshTunnelStatus()
            }
        }

        pathMonitor.pathUpdateHandler = { [weak self] path in
            let description: String
            if path.status != .satisfied {
                description = "No internet path"
            } else if path.usesInterfaceType(.cellular) {
                description = "Cellular"
            } else if path.usesInterfaceType(.wifi) {
                description = "Wi-Fi"
            } else if path.usesInterfaceType(.wiredEthernet) {
                description = "Ethernet"
            } else {
                description = "VPN or other"
            }

            Task { @MainActor in
                self?.networkDescription = description
            }
        }
        pathMonitor.start(queue: monitorQueue)
        loadManager()
    }

    deinit {
        if let statusObserver {
            NotificationCenter.default.removeObserver(statusObserver)
        }
        pathMonitor.cancel()
    }

    var isTunnelActive: Bool {
        tunnelStatus == .connected
    }

    var isTunnelChanging: Bool {
        tunnelStatus == .connecting || tunnelStatus == .disconnecting || tunnelStatus == .reasserting
    }

    var tunnelButtonTitle: String {
        switch tunnelStatus {
        case .connected, .connecting, .reasserting:
            return "Stop local reflector"
        default:
            return "Enable local reflector"
        }
    }

    var tunnelDescription: String {
        switch tunnelStatus {
        case .invalid: return "Not configured"
        case .disconnected: return "Off"
        case .connecting: return "Connecting…"
        case .connected: return "Connected"
        case .reasserting: return "Reconnecting…"
        case .disconnecting: return "Stopping…"
        @unknown default: return "Unknown"
        }
    }

    var tunnelColor: Color {
        switch tunnelStatus {
        case .connected: return .green
        case .connecting, .reasserting, .disconnecting: return .orange
        default: return .gray
        }
    }

    var internetColor: Color {
        resultColor(for: internetResult)
    }

    var pairingColor: Color {
        resultColor(for: pairingResult)
    }

    var lockdownColor: Color {
        resultColor(for: lockdownResult)
    }

    var resultSymbol: String {
        if internetResult == "Passed" && lockdownResult.hasPrefix("Passed") {
            return "checkmark.seal.fill"
        }
        if internetResult == "Not run" && pairingResult == "Not run" && lockdownResult == "Not run" {
            return "questionmark.circle"
        }
        return "exclamationmark.triangle.fill"
    }

    func toggleTunnel() {
        if tunnelStatus == .connected || tunnelStatus == .connecting || tunnelStatus == .reasserting {
            manager?.connection.stopVPNTunnel()
            return
        }

        if let manager {
            start(manager)
        } else {
            configureAndStart()
        }
    }

    func runProof() {
        guard isTunnelActive, !isRunningProof else { return }
        isRunningProof = true
        internetResult = "Checking…"
        pairingResult = "Checking…"
        lockdownResult = "Checking…"
        resultSummary = "Checking the public internet and both on-device Apple service paths."
        diagnosticResult = "Recording…"

        let runID = ProbeDiagnostics.shared.start(context: [
            "network": networkDescription,
            "tunnel_status": tunnelDescription,
        ])

        captureTunnelDiagnostics(runID: runID, event: "tunnel.baseline") { [weak self] _ in
            Task { @MainActor in
                self?.performProof(runID: runID)
            }
        }
    }

    private func performProof(runID: String) {
        let group = DispatchGroup()
        var internetSucceeded = false
        var pairingReport: RemotePairingReport?
        var pairingError: Error?
        var lockdownReport: LockdownReport?
        var lockdownError: Error?

        group.enter()
        InternetProbe.run(runID: runID) { result in
            if case .success = result {
                internetSucceeded = true
            }
            group.leave()
        }

        group.enter()
        RemotePairingProbe.run(runID: runID) { result in
            switch result {
            case .success(let report):
                pairingReport = report
            case .failure(let error):
                pairingError = error
            }
            group.leave()
        }

        group.enter()
        LockdownProbe.run(runID: runID) { result in
            switch result {
            case .success(let report):
                lockdownReport = report
            case .failure(let error):
                lockdownError = error
            }
            group.leave()
        }

        group.notify(queue: .main) { [weak self] in
            guard let self else { return }
            self.internetResult = internetSucceeded ? "Passed" : "Failed"

            if let report = pairingReport {
                self.pairingResult = "Passed · protocol \(report.wireProtocolVersion)"
            } else {
                self.pairingResult = "Failed"
            }

            self.lockdownResult = lockdownReport == nil ? "Failed" : "Passed · valid plist"

            if internetSucceeded, lockdownReport != nil {
                let pairingNote = pairingReport == nil ? " RemotePairing 49152 still failed, as expected: \(pairingError?.localizedDescription ?? "unknown error")" : " RemotePairing 49152 also answered."
                self.resultSummary = "Promising: classic lockdownd answered on 62078 while cellular internet remained active. This is the prerequisite for a saved-trust-record and CoreDeviceProxy installer.\(pairingNote)"
            } else if internetSucceeded {
                self.resultSummary = "Cellular internet works, but neither usable Apple service path answered. Lockdown 62078: \(lockdownError?.localizedDescription ?? "failed"). RemotePairing 49152: \(pairingError?.localizedDescription ?? "failed")."
            } else if lockdownReport != nil {
                self.resultSummary = "Classic lockdownd answered, but the public internet check failed. A cellular build-delivery path cannot work in this state."
            } else {
                self.resultSummary = "Internet and lockdown both failed. Lockdown 62078: \(lockdownError?.localizedDescription ?? "failed")."
            }

            self.captureTunnelDiagnostics(runID: runID, event: "tunnel.final") { [weak self] didCapture in
                let result = internetSucceeded && lockdownReport != nil ? "succeeded" : "failed"
                let didSave = ProbeDiagnostics.shared.complete(runID: runID, result: result)
                Task { @MainActor in
                    self?.isRunningProof = false
                    self?.diagnosticResult = didSave && didCapture ? "Saved" : "Saved · partial"
                }
            }
        }
    }

    private func captureTunnelDiagnostics(
        runID: String,
        event: String,
        completion: @escaping (Bool) -> Void
    ) {
        guard let session = manager?.connection as? NETunnelProviderSession else {
            ProbeDiagnostics.shared.record(
                event,
                result: "failed",
                runID: runID,
                context: ["reason": "session_unavailable"]
            )
            completion(false)
            return
        }

        do {
            try session.sendProviderMessage(Data("diagnosticSnapshot".utf8)) { data in
                guard
                    let data,
                    let snapshot = try? JSONSerialization.jsonObject(with: data) as? [String: Any]
                else {
                    ProbeDiagnostics.shared.record(
                        event,
                        result: "failed",
                        runID: runID,
                        context: ["reason": "invalid_response"]
                    )
                    completion(false)
                    return
                }

                let context = snapshot.reduce(into: [String: String]()) { result, pair in
                    result[pair.key] = String(describing: pair.value)
                }
                ProbeDiagnostics.shared.record(
                    event,
                    result: "succeeded",
                    runID: runID,
                    context: context
                )
                completion(true)
            }
        } catch {
            ProbeDiagnostics.shared.record(
                event,
                result: "failed",
                runID: runID,
                context: ProbeDiagnostics.safeErrorContext(error)
            )
            completion(false)
        }
    }

    private func loadManager() {
        NETunnelProviderManager.loadAllFromPreferences { [weak self] managers, _ in
            Task { @MainActor in
                guard let self else { return }
                self.manager = managers?.first(where: self.owns)
                self.refreshTunnelStatus()
            }
        }
    }

    private func configureAndStart() {
        let manager = NETunnelProviderManager()
        let configuration = NETunnelProviderProtocol()
        configuration.providerBundleIdentifier = providerBundleIdentifier
        configuration.serverAddress = "On-device pairing reflector"
        manager.protocolConfiguration = configuration
        manager.localizedDescription = "Xcode Cellular Probe"
        manager.isEnabled = true

        manager.saveToPreferences { [weak self, weak manager] error in
            guard let manager else { return }
            Task { @MainActor in
                guard let self else { return }
                if let error {
                    self.resultSummary = "Could not save the VPN configuration: \(error.localizedDescription)"
                    return
                }
                self.manager = manager
                manager.loadFromPreferences { [weak self, weak manager] error in
                    Task { @MainActor in
                        guard let self, let manager else { return }
                        if let error {
                            self.resultSummary = "Could not load the VPN configuration: \(error.localizedDescription)"
                            return
                        }
                        self.start(manager)
                    }
                }
            }
        }
    }

    private func start(_ manager: NETunnelProviderManager) {
        do {
            try manager.connection.startVPNTunnel()
            tunnelStatus = .connecting
            resultSummary = "The local reflector is starting."
        } catch {
            resultSummary = "Could not start the local reflector: \(error.localizedDescription)"
            refreshTunnelStatus()
        }
    }

    private func refreshTunnelStatus() {
        tunnelStatus = manager?.connection.status ?? .invalid
    }

    private func owns(_ manager: NETunnelProviderManager) -> Bool {
        guard let configuration = manager.protocolConfiguration as? NETunnelProviderProtocol else {
            return false
        }
        return configuration.providerBundleIdentifier == providerBundleIdentifier
    }

    private func resultColor(for value: String) -> Color {
        if value == "Passed" || value.hasPrefix("Passed") {
            return .green
        }
        if value == "Checking…" {
            return .orange
        }
        if value == "Not run" {
            return .gray
        }
        return .red
    }
}
