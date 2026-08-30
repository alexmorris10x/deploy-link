import SwiftUI

struct ContentView: View {
    @EnvironmentObject private var model: PrototypeModel

    var body: some View {
        NavigationStack {
            ScrollView {
                VStack(alignment: .leading, spacing: 20) {
                    Text("Cellular-only Xcode proof")
                        .font(.largeTitle.bold())

                    Text("This checks two Apple device-service paths while the iPhone still has an internet connection. It does not change the audiobook app.")
                        .foregroundStyle(.secondary)

                    instructionCard
                    statusCard

                    Button(action: model.toggleTunnel) {
                        Label(model.tunnelButtonTitle, systemImage: model.isTunnelActive ? "stop.circle" : "network.badge.shield.half.filled")
                            .frame(maxWidth: .infinity)
                    }
                    .buttonStyle(.borderedProminent)
                    .controlSize(.large)
                    .disabled(model.isTunnelChanging)

                    Button(action: model.runProof) {
                        Label(model.isRunningProof ? "Running proof…" : "Run cellular proof", systemImage: "waveform.path.ecg")
                            .frame(maxWidth: .infinity)
                    }
                    .buttonStyle(.bordered)
                    .controlSize(.large)
                    .disabled(!model.isTunnelActive || model.isRunningProof)

                    resultCard
                }
                .padding(20)
            }
            .navigationTitle("Xcode Probe")
            .navigationBarTitleDisplayMode(.inline)
        }
    }

    private var instructionCard: some View {
        VStack(alignment: .leading, spacing: 10) {
            Label("Before you run it", systemImage: "list.number")
                .font(.headline)

            Text("1. Turn Wi-Fi off.")
            Text("2. Keep cellular data on.")
            Text("3. Tap Enable local reflector and allow the VPN configuration.")
            Text("4. Tap Run cellular proof.")

            Text("Starting this reflector replaces Tailscale’s VPN connection during the test.")
                .font(.footnote)
                .foregroundStyle(.secondary)
        }
        .prototypeCard()
    }

    private var statusCard: some View {
        VStack(alignment: .leading, spacing: 12) {
            Label("Current state", systemImage: "iphone.gen3")
                .font(.headline)

            StatusRow(label: "Network", value: model.networkDescription, color: .blue)
            StatusRow(label: "Local reflector", value: model.tunnelDescription, color: model.tunnelColor)
        }
        .prototypeCard()
    }

    private var resultCard: some View {
        VStack(alignment: .leading, spacing: 12) {
            Label("Proof result", systemImage: model.resultSymbol)
                .font(.headline)

            StatusRow(label: "Internet", value: model.internetResult, color: model.internetColor)
            StatusRow(label: "RemotePairing 49152", value: model.pairingResult, color: model.pairingColor)
            StatusRow(label: "Lockdown 62078", value: model.lockdownResult, color: model.lockdownColor)
            StatusRow(
                label: "Diagnostic",
                value: model.diagnosticResult,
                color: model.diagnosticResult == "Saved" ? .green :
                    (model.diagnosticResult == "Recording…" ? .orange : .gray)
            )

            Text(model.resultSummary)
                .font(.footnote)
                .foregroundStyle(.secondary)
                .textSelection(.enabled)

            Text("Only the latest run is kept. No pairing record, traffic content, URL, or device identifier is logged.")
                .font(.caption)
                .foregroundStyle(.tertiary)
        }
        .prototypeCard()
    }
}

private struct StatusRow: View {
    let label: String
    let value: String
    let color: Color

    var body: some View {
        HStack(alignment: .firstTextBaseline) {
            Circle()
                .fill(color)
                .frame(width: 9, height: 9)
            Text(label)
                .foregroundStyle(.secondary)
            Spacer(minLength: 12)
            Text(value)
                .multilineTextAlignment(.trailing)
        }
        .font(.subheadline)
    }
}

private extension View {
    func prototypeCard() -> some View {
        padding(16)
            .frame(maxWidth: .infinity, alignment: .leading)
            .background(.thinMaterial, in: RoundedRectangle(cornerRadius: 18, style: .continuous))
    }
}
