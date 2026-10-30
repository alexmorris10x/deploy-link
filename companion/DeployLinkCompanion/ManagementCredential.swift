import Foundation
import Security

enum ManagementCredential {
    private static var service: String { (Bundle.main.bundleIdentifier ?? "DeployLink") + ".management" }

    private static func query(_ server: URL) -> [String: Any] {
        [kSecClass as String: kSecClassGenericPassword,
         kSecAttrService as String: service,
         kSecAttrAccount as String: server.absoluteString]
    }

    static func read(for server: URL) -> String? {
        var request = query(server)
        request[kSecReturnData as String] = true
        request[kSecMatchLimit as String] = kSecMatchLimitOne
        var result: CFTypeRef?
        guard SecItemCopyMatching(request as CFDictionary, &result) == errSecSuccess,
              let data = result as? Data else { return nil }
        return String(data: data, encoding: .utf8)
    }

    static func save(_ token: String, for server: URL) -> Bool {
        let attributes: [String: Any] = [kSecValueData as String: Data(token.utf8),
                                        kSecAttrAccessible as String: kSecAttrAccessibleWhenUnlockedThisDeviceOnly]
        let status = SecItemUpdate(query(server) as CFDictionary, attributes as CFDictionary)
        if status == errSecSuccess { return true }
        guard status == errSecItemNotFound else { return false }
        return SecItemAdd(query(server).merging(attributes) { _, new in new } as CFDictionary, nil) == errSecSuccess
    }

    static func remove(for server: URL) -> Bool {
        let status = SecItemDelete(query(server) as CFDictionary)
        return status == errSecSuccess || status == errSecItemNotFound
    }
}

/// Keep a server's management credential on its selected HTTPS origin.
final class ServerRedirectPolicy: NSObject, URLSessionTaskDelegate {
    func urlSession(_ session: URLSession, task: URLSessionTask,
                    willPerformHTTPRedirection response: HTTPURLResponse,
                    newRequest request: URLRequest,
                    completionHandler: @escaping (URLRequest?) -> Void) {
        guard let original = task.originalRequest?.url, let target = request.url,
              ServerAddress.sameOrigin(original, target) else { completionHandler(nil); return }
        completionHandler(request)
    }
}
