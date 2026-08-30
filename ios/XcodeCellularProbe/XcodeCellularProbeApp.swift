import SwiftUI

@main
struct XcodeCellularProbeApp: App {
    @StateObject private var model = PrototypeModel()

    var body: some Scene {
        WindowGroup {
            ContentView()
                .environmentObject(model)
        }
    }
}
