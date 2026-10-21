import Foundation
import Observation
import UIKit

/// One build waiting in the Mac's OTA queue (`ota/ota_queue.py`, `/api/releases`).
struct Release: Decodable, Identifiable, Hashable {
    let id: String
    let title: String
    let bundleIdentifier: String
    let version: String
    let build: String
    let note: String
    let createdAt: String
    let ipaBytes: Int
    let profileKind: String?
    let profileExpires: String?
    let iconURL: URL?
    let manifestURL: URL
    let downloadCount: Int
    /// Latest completed download per device family ("iPhone", "iPad"), newest first.
    let downloadedBy: [DeviceDownload]?
    /// Downloads the Mac is sending right now.
    let activeDownloads: [ActiveDownload]?

    struct DeviceDownload: Decodable, Hashable {
        let device: String
        let at: String
    }

    struct ActiveDownload: Decodable, Hashable {
        let deviceId: String?
        let device: String
        let bytes: Int
        let totalBytes: Int
    }

    var created: Date? { ISO8601DateFormatter().date(from: createdAt) }
    var isThisApp: Bool { bundleIdentifier == Bundle.main.bundleIdentifier }
    var versionLabel: String { "\(version) (\(build))" }
}

/// The build of an app this device last installed from the queue.
struct InstalledApp: Decodable, Hashable {
    let releaseId: String
    let version: String
    let build: String
    let at: String
    /// Whether that build is still in the queue.
    let available: Bool

    var date: Date? { ISO8601DateFormatter().date(from: at) }
    var versionLabel: String { "\(version) (\(build))" }
}

private struct ReleaseFeed: Decodable {
    let releases: [Release]
    let installed: [String: InstalledApp]?
}

enum AppInfo {
    static let version = Bundle.main.object(forInfoDictionaryKey: "CFBundleShortVersionString") as? String ?? "?"
    static let build = Bundle.main.object(forInfoDictionaryKey: "CFBundleVersion") as? String ?? "?"
}

/// What the Install button can do for one release on this device.
enum InstallState: Equatable {
    case install
    /// A newer build than the one on this device.
    case update
    /// This exact build is already on this device; reinstalling needs a confirmation.
    case installed
    /// Older than the build on this device; installing needs a confirmation.
    case older
    /// Install was tapped; waiting for the iOS prompt to be confirmed.
    case waiting
    /// The Mac is sending this build to this device.
    case downloading(Double)
    /// Another build of the same app is on its way to this device.
    case busy
    /// The Deploy Link build that is running right now.
    case running
}

@MainActor
@Observable
final class ReleaseStore {
    static let defaultServer = "https://10x-macbook-air.tailb523d4.ts.net"
    private static let serverKey = "serverURL"
    private static let deviceKey = "deviceID"
    /// How long a tapped Install waits for its download to start before the button frees up again
    /// (the iOS prompt may have been cancelled).
    private static let promptWindow: TimeInterval = 60

    enum LoadState: Equatable {
        case idle
        case loading
        case loaded(Date)
        case failed(String)
    }

    var releases: [Release] = []
    /// Per bundle identifier, the build this device last installed from the queue.
    var installed: [String: InstalledApp] = [:]
    var state: LoadState = .idle
    /// A message for an alert, such as a build that left the queue.
    var notice: String?
    /// Install taps by bundle identifier: which release and when.
    private var requests: [String: (releaseId: String, at: Date)] = [:]

    var serverURL: String {
        didSet { UserDefaults.standard.set(serverURL, forKey: Self.serverKey) }
    }

    /// Identifies this device in install links, so the Mac knows which download belongs to whom.
    let deviceID: String
    let family = UIDevice.current.userInterfaceIdiom == .pad ? "iPad" : "iPhone"

    private let session: URLSession = {
        let configuration = URLSessionConfiguration.ephemeral
        configuration.timeoutIntervalForRequest = 10
        configuration.requestCachePolicy = .reloadIgnoringLocalCacheData
        configuration.waitsForConnectivity = false
        return URLSession(configuration: configuration)
    }()

    init() {
        serverURL = UserDefaults.standard.string(forKey: Self.serverKey) ?? Self.defaultServer
        if let saved = UserDefaults.standard.string(forKey: Self.deviceKey) {
            deviceID = saved
        } else {
            deviceID = UUID().uuidString
            UserDefaults.standard.set(deviceID, forKey: Self.deviceKey)
        }
    }

    /// Releases grouped by app, the app with the newest build first; each group newest first.
    var apps: [[Release]] {
        var order: [String] = []
        var groups: [String: [Release]] = [:]
        for release in releases {
            if groups[release.bundleIdentifier] == nil { order.append(release.bundleIdentifier) }
            groups[release.bundleIdentifier, default: []].append(release)
        }
        return order.compactMap { groups[$0] }
    }

    /// The build of an app on this device, as far as Deploy Link knows.
    func installedBuild(of bundleIdentifier: String) -> InstalledApp? {
        if bundleIdentifier == Bundle.main.bundleIdentifier {
            let match = releases.first { $0.bundleIdentifier == bundleIdentifier && $0.build == AppInfo.build }
            return InstalledApp(releaseId: match?.id ?? "", version: AppInfo.version, build: AppInfo.build, at: "", available: match != nil)
        }
        return installed[bundleIdentifier]
    }

    func installState(of release: Release) -> InstallState {
        if release.isThisApp && release.build == AppInfo.build { return .running }
        if let download = activeDownload(of: release) {
            return .downloading(download.totalBytes > 0 ? Double(download.bytes) / Double(download.totalBytes) : 0)
        }
        let siblings = releases.filter { $0.bundleIdentifier == release.bundleIdentifier && $0.id != release.id }
        if siblings.contains(where: { activeDownload(of: $0) != nil }) { return .busy }
        if let request = requests[release.bundleIdentifier], Date.now.timeIntervalSince(request.at) < Self.promptWindow,
           !finished(request) {
            return request.releaseId == release.id ? .waiting : .busy
        }
        guard let current = installedBuild(of: release.bundleIdentifier) else { return .install }
        switch release.build.compare(current.build, options: .numeric) {
        case .orderedSame: return .installed
        case .orderedAscending: return .older
        case .orderedDescending: return .update
        }
    }

    private func activeDownload(of release: Release) -> Release.ActiveDownload? {
        release.activeDownloads?.first { $0.deviceId == deviceID }
    }

    /// Whether the build requested here has since finished downloading to this device.
    private func finished(_ request: (releaseId: String, at: Date)) -> Bool {
        installed.values.contains { $0.releaseId == request.releaseId && ($0.date ?? .distantPast) >= request.at.addingTimeInterval(-5) }
    }

    func refresh() async {
        guard let base = baseURL else {
            state = .failed("The server address must start with https://")
            return
        }
        if releases.isEmpty { state = .loading }
        do {
            var components = URLComponents(url: base.appending(path: "api/releases"), resolvingAgainstBaseURL: false)!
            components.queryItems = [URLQueryItem(name: "device", value: deviceID), URLQueryItem(name: "family", value: family)]
            let (data, response) = try await session.data(from: components.url!)
            guard let http = response as? HTTPURLResponse, http.statusCode == 200 else {
                throw URLError(.badServerResponse)
            }
            let feed = try JSONDecoder().decode(ReleaseFeed.self, from: data)
            releases = feed.releases
            installed = feed.installed ?? [:]
            requests = requests.filter { _, request in
                Date.now.timeIntervalSince(request.at) < Self.promptWindow && !finished(request)
            }
            state = .loaded(.now)
        } catch is CancellationError {
            return
        } catch let error as URLError where error.code == .cancelled {
            return
        } catch {
            state = .failed(Self.describe(error))
        }
    }

    /// Hands the release's install link to iOS, after checking the Mac still has it.
    func install(_ release: Release) async {
        guard await stillAvailable(release) else {
            notice = "\(release.title) \(release.versionLabel) is no longer on the Mac, so it was taken off the list."
            await refresh()
            return
        }
        var manifest = URLComponents(url: release.manifestURL, resolvingAgainstBaseURL: false)!
        manifest.queryItems = [URLQueryItem(name: "device", value: deviceID), URLQueryItem(name: "family", value: family)]
        // The manifest URL carries its own query, so escape everything but letters and digits.
        guard let encoded = manifest.url?.absoluteString.addingPercentEncoding(withAllowedCharacters: .alphanumerics),
              let url = URL(string: "itms-services://?action=download-manifest&url=" + encoded) else { return }
        requests[release.bundleIdentifier] = (release.id, .now)
        await UIApplication.shared.open(url)
    }

    /// Removes a release from the Mac's queue.
    func delete(_ release: Release) async {
        guard let base = baseURL else { return }
        var request = URLRequest(url: base.appending(path: "api/releases/\(release.id)"))
        request.httpMethod = "DELETE"
        do {
            let (_, response) = try await session.data(for: request)
            let status = (response as? HTTPURLResponse)?.statusCode ?? 0
            if status != 200 && status != 404 { throw URLError(.badServerResponse) }
            releases.removeAll { $0.id == release.id }
        } catch {
            notice = "Couldn't remove \(release.title) \(release.versionLabel): \(Self.describe(error))"
        }
        await refresh()
    }

    private func stillAvailable(_ release: Release) async -> Bool {
        var request = URLRequest(url: release.manifestURL)
        request.httpMethod = "HEAD"
        guard let (_, response) = try? await session.data(for: request) else { return true }  // let iOS report network trouble
        return (response as? HTTPURLResponse)?.statusCode != 404
    }

    private var baseURL: URL? {
        guard let base = URL(string: serverURL.trimmingCharacters(in: .whitespacesAndNewlines)), base.scheme == "https" else {
            return nil
        }
        return base
    }

    private static func describe(_ error: Error) -> String {
        if let error = error as? URLError {
            switch error.code {
            case .notConnectedToInternet, .networkConnectionLost:
                return "This device is offline."
            case .timedOut, .cannotFindHost, .cannotConnectToHost, .dnsLookupFailed:
                return "Can't reach the Mac. Check that Tailscale is connected on this device and the Mac is awake."
            case .badServerResponse:
                return "The Mac answered, but the build queue isn't running."
            default:
                break
            }
        }
        if error is DecodingError {
            return "The Mac sent a build list this version doesn't understand."
        }
        return error.localizedDescription
    }
}
