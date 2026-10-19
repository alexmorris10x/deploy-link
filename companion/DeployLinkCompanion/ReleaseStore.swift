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
    let installURL: URL
    let downloadCount: Int
    let lastDownloadedAt: String?

    var created: Date? { ISO8601DateFormatter().date(from: createdAt) }
    var lastDownloaded: Date? { lastDownloadedAt.flatMap { ISO8601DateFormatter().date(from: $0) } }

    /// True when this release is the app that is running right now.
    var isThisApp: Bool { bundleIdentifier == Bundle.main.bundleIdentifier }
    var isRunningBuild: Bool { isThisApp && build == AppInfo.build }
}

private struct ReleaseFeed: Decodable {
    let releases: [Release]
}

enum AppInfo {
    static let version = Bundle.main.object(forInfoDictionaryKey: "CFBundleShortVersionString") as? String ?? "?"
    static let build = Bundle.main.object(forInfoDictionaryKey: "CFBundleVersion") as? String ?? "?"
}

@MainActor
@Observable
final class ReleaseStore {
    static let defaultServer = "https://10x-macbook-air.tailb523d4.ts.net"
    private static let serverKey = "serverURL"

    enum LoadState: Equatable {
        case idle
        case loading
        case loaded(Date)
        case failed(String)
    }

    var releases: [Release] = []
    var state: LoadState = .idle
    /// Releases whose install link was handed to iOS, with when.
    var installRequests: [String: Date] = [:]

    var serverURL: String {
        didSet { UserDefaults.standard.set(serverURL, forKey: Self.serverKey) }
    }

    private let session: URLSession = {
        let configuration = URLSessionConfiguration.ephemeral
        configuration.timeoutIntervalForRequest = 10
        configuration.requestCachePolicy = .reloadIgnoringLocalCacheData
        configuration.waitsForConnectivity = false
        return URLSession(configuration: configuration)
    }()

    init() {
        serverURL = UserDefaults.standard.string(forKey: Self.serverKey) ?? Self.defaultServer
    }

    func refresh() async {
        guard let base = URL(string: serverURL.trimmingCharacters(in: .whitespacesAndNewlines)),
              base.scheme == "https" else {
            state = .failed("The server address must start with https://")
            return
        }
        if releases.isEmpty { state = .loading }
        do {
            let (data, response) = try await session.data(from: base.appending(path: "api/releases"))
            guard let http = response as? HTTPURLResponse, http.statusCode == 200 else {
                throw URLError(.badServerResponse)
            }
            releases = try JSONDecoder().decode(ReleaseFeed.self, from: data).releases
            state = .loaded(.now)
        } catch is CancellationError {
            return
        } catch let error as URLError where error.code == .cancelled {
            return
        } catch {
            state = .failed(Self.describe(error))
        }
    }

    func install(_ release: Release) {
        installRequests[release.id] = .now
        UIApplication.shared.open(release.installURL)
    }

    private static func describe(_ error: Error) -> String {
        if let error = error as? URLError {
            switch error.code {
            case .notConnectedToInternet, .networkConnectionLost:
                return "This iPhone is offline."
            case .timedOut, .cannotFindHost, .cannotConnectToHost, .dnsLookupFailed:
                return "Can't reach the Mac. Check that Tailscale is connected on this iPhone and the Mac is awake."
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
