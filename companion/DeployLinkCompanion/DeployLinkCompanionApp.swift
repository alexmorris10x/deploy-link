import SwiftUI

@main
struct DeployLinkCompanionApp: App {
    @State private var store = ReleaseStore()

    var body: some Scene {
        WindowGroup {
            ReleaseListView()
                .environment(store)
        }
    }
}
