import Foundation

enum ServerAddress {
    static func parse(_ text: String) -> URL? {
        guard var parts = URLComponents(string: text.trimmingCharacters(in: .whitespacesAndNewlines)),
              parts.scheme?.lowercased() == "https", let host = parts.host, !host.isEmpty,
              parts.user == nil, parts.password == nil, parts.query == nil, parts.fragment == nil,
              parts.path.isEmpty || parts.path == "/" else { return nil }
        parts.scheme = "https"
        parts.host = host.lowercased()
        parts.path = ""
        return parts.url
    }

    static func sameOrigin(_ lhs: URL, _ rhs: URL) -> Bool {
        lhs.scheme?.lowercased() == "https" && rhs.scheme?.lowercased() == "https"
            && lhs.host?.lowercased() == rhs.host?.lowercased()
            && (lhs.port ?? 443) == (rhs.port ?? 443)
            && lhs.user == nil && lhs.password == nil
            && rhs.user == nil && rhs.password == nil
    }
}
