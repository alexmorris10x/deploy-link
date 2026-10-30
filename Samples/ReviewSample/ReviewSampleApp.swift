import SwiftUI

@main
struct ReviewSampleApp: App {
    var body: some Scene {
        WindowGroup {
            ReviewSampleView()
        }
    }
}

private struct ReviewSampleView: View {
    @State private var tapCount = 0

    var body: some View {
        VStack(spacing: 24) {
            Image(systemName: "checkmark.circle.fill")
                .font(.system(size: 72))
                .foregroundStyle(.green)
            Text("Sample opened")
                .font(.largeTitle.bold())
            Text("This is the standalone Deploy Link review sample.")
                .multilineTextAlignment(.center)
            Text("Version \(Bundle.main.object(forInfoDictionaryKey: "CFBundleShortVersionString") as? String ?? "1.0") · Build \(Bundle.main.object(forInfoDictionaryKey: "CFBundleVersion") as? String ?? "1")")
                .font(.subheadline)
                .foregroundStyle(.secondary)
            Button("Tap to test · \(tapCount)") {
                tapCount += 1
            }
            .buttonStyle(.borderedProminent)
            .accessibilityIdentifier("review-sample.test-button")
            Text("No account, network requests, permissions, purchases, or saved data.")
                .font(.footnote)
                .foregroundStyle(.secondary)
                .multilineTextAlignment(.center)
        }
        .padding(32)
    }
}
