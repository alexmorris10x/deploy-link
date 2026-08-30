import Foundation
import NetworkExtension

// Packet reflection pattern based on LocalDevVPN:
// https://github.com/jkcoxson/LocalDevVPN
final class PacketTunnelProvider: NEPacketTunnelProvider {
    private let interfaceAddress = "10.7.1.1"
    private let peerAddress = "10.7.0.1"
    private let providerGeneration = UUID().uuidString
    private let statisticsLock = NSLock()
    private var settingsApplied = false
    private var packetBatchesRead = 0
    private var packetsRead = 0
    private var ipv4PacketsReflected = 0
    private var writeCalls = 0
    private var rejectedWriteCalls = 0
    private var synToPairingPort = 0
    private var synAckFromPairingPort = 0
    private var resetFromPairingPort = 0
    private var clientPayloadBytes = 0
    private var servicePayloadBytes = 0
    private var synToLockdownPort = 0
    private var synAckFromLockdownPort = 0
    private var resetFromLockdownPort = 0
    private var lockdownClientPayloadBytes = 0
    private var lockdownServicePayloadBytes = 0

    override func startTunnel(
        options: [String: NSObject]?,
        completionHandler: @escaping (Error?) -> Void
    ) {
        let ipv4 = NEIPv4Settings(
            addresses: [interfaceAddress],
            subnetMasks: ["255.255.255.255"]
        )
        ipv4.includedRoutes = [
            NEIPv4Route(destinationAddress: peerAddress, subnetMask: "255.255.255.255")
        ]
        ipv4.excludedRoutes = [.default()]

        let settings = NEPacketTunnelNetworkSettings(tunnelRemoteAddress: peerAddress)
        settings.ipv4Settings = ipv4

        setTunnelNetworkSettings(settings) { [weak self] error in
            guard error == nil, let self else {
                completionHandler(error)
                return
            }
            self.statisticsLock.lock()
            self.settingsApplied = true
            self.statisticsLock.unlock()
            self.reflectPackets()
            completionHandler(nil)
        }
    }

    override func stopTunnel(
        with reason: NEProviderStopReason,
        completionHandler: @escaping () -> Void
    ) {
        completionHandler()
    }

    override func handleAppMessage(
        _ messageData: Data,
        completionHandler: ((Data?) -> Void)? = nil
    ) {
        guard String(data: messageData, encoding: .utf8) == "diagnosticSnapshot" else {
            completionHandler?(nil)
            return
        }

        statisticsLock.lock()
        let snapshot: [String: Any] = [
            "settings_applied": settingsApplied,
            "packet_batches_read": packetBatchesRead,
            "packets_read": packetsRead,
            "ipv4_packets_reflected": ipv4PacketsReflected,
            "write_calls": writeCalls,
            "rejected_write_calls": rejectedWriteCalls,
            "syn_to_49152": synToPairingPort,
            "syn_ack_from_49152": synAckFromPairingPort,
            "rst_from_49152": resetFromPairingPort,
            "client_payload_bytes": clientPayloadBytes,
            "service_payload_bytes": servicePayloadBytes,
            "syn_to_62078": synToLockdownPort,
            "syn_ack_from_62078": synAckFromLockdownPort,
            "rst_from_62078": resetFromLockdownPort,
            "lockdown_client_payload_bytes": lockdownClientPayloadBytes,
            "lockdown_service_payload_bytes": lockdownServicePayloadBytes,
            "provider_generation": providerGeneration,
        ]
        statisticsLock.unlock()

        completionHandler?(try? JSONSerialization.data(withJSONObject: snapshot, options: [.sortedKeys]))
    }

    private func reflectPackets() {
        packetFlow.readPackets { [weak self] packets, protocols in
            guard let self else { return }
            var reflectedPackets = packets
            var reflectedIPv4Count = 0
            var batchSYNToPairingPort = 0
            var batchSYNAckFromPairingPort = 0
            var batchResetFromPairingPort = 0
            var batchClientPayloadBytes = 0
            var batchServicePayloadBytes = 0
            var batchSYNToLockdownPort = 0
            var batchSYNAckFromLockdownPort = 0
            var batchResetFromLockdownPort = 0
            var batchLockdownClientPayloadBytes = 0
            var batchLockdownServicePayloadBytes = 0

            for index in reflectedPackets.indices
            where protocols[index].int32Value == AF_INET && reflectedPackets[index].count >= 20 {
                if let details = self.tcpDetails(in: reflectedPackets[index]) {
                    if details.destinationPort == 49152, details.isSYN, !details.isACK {
                        batchSYNToPairingPort += 1
                    }
                    if details.sourcePort == 49152, details.isSYN, details.isACK {
                        batchSYNAckFromPairingPort += 1
                    }
                    if details.sourcePort == 49152, details.isRST {
                        batchResetFromPairingPort += 1
                    }
                    if details.destinationPort == 49152 {
                        batchClientPayloadBytes += details.payloadBytes
                    }
                    if details.sourcePort == 49152 {
                        batchServicePayloadBytes += details.payloadBytes
                    }
                    if details.destinationPort == 62078, details.isSYN, !details.isACK {
                        batchSYNToLockdownPort += 1
                    }
                    if details.sourcePort == 62078, details.isSYN, details.isACK {
                        batchSYNAckFromLockdownPort += 1
                    }
                    if details.sourcePort == 62078, details.isRST {
                        batchResetFromLockdownPort += 1
                    }
                    if details.destinationPort == 62078 {
                        batchLockdownClientPayloadBytes += details.payloadBytes
                    }
                    if details.sourcePort == 62078 {
                        batchLockdownServicePayloadBytes += details.payloadBytes
                    }
                }

                reflectedPackets[index].withUnsafeMutableBytes { bytes in
                    guard let words = bytes.baseAddress?.assumingMemoryBound(to: UInt32.self) else {
                        return
                    }
                    let source = words[3]
                    words[3] = words[4]
                    words[4] = source
                }
                reflectedIPv4Count += 1
            }

            let writeAccepted = self.packetFlow.writePackets(reflectedPackets, withProtocols: protocols)
            self.statisticsLock.lock()
            self.packetBatchesRead += 1
            self.packetsRead += packets.count
            self.ipv4PacketsReflected += reflectedIPv4Count
            self.writeCalls += 1
            self.synToPairingPort += batchSYNToPairingPort
            self.synAckFromPairingPort += batchSYNAckFromPairingPort
            self.resetFromPairingPort += batchResetFromPairingPort
            self.clientPayloadBytes += batchClientPayloadBytes
            self.servicePayloadBytes += batchServicePayloadBytes
            self.synToLockdownPort += batchSYNToLockdownPort
            self.synAckFromLockdownPort += batchSYNAckFromLockdownPort
            self.resetFromLockdownPort += batchResetFromLockdownPort
            self.lockdownClientPayloadBytes += batchLockdownClientPayloadBytes
            self.lockdownServicePayloadBytes += batchLockdownServicePayloadBytes
            if !writeAccepted {
                self.rejectedWriteCalls += 1
            }
            self.statisticsLock.unlock()
            self.reflectPackets()
        }
    }

    private struct TCPDetails {
        let sourcePort: UInt16
        let destinationPort: UInt16
        let isSYN: Bool
        let isACK: Bool
        let isRST: Bool
        let payloadBytes: Int
    }

    private func tcpDetails(in packet: Data) -> TCPDetails? {
        packet.withUnsafeBytes { rawBytes in
            guard let bytes = rawBytes.bindMemory(to: UInt8.self).baseAddress else { return nil }
            guard packet.count >= 20, bytes[0] >> 4 == 4, bytes[9] == 6 else { return nil }

            let ipHeaderBytes = Int(bytes[0] & 0x0F) * 4
            guard ipHeaderBytes >= 20, packet.count >= ipHeaderBytes + 20 else { return nil }

            let sourcePort = UInt16(bytes[ipHeaderBytes]) << 8 | UInt16(bytes[ipHeaderBytes + 1])
            let destinationPort = UInt16(bytes[ipHeaderBytes + 2]) << 8 | UInt16(bytes[ipHeaderBytes + 3])
            let tcpHeaderBytes = Int(bytes[ipHeaderBytes + 12] >> 4) * 4
            guard tcpHeaderBytes >= 20, packet.count >= ipHeaderBytes + tcpHeaderBytes else { return nil }

            let flags = bytes[ipHeaderBytes + 13]
            let totalLength = Int(bytes[2]) << 8 | Int(bytes[3])
            let boundedLength = min(totalLength, packet.count)
            let payloadBytes = max(0, boundedLength - ipHeaderBytes - tcpHeaderBytes)

            return TCPDetails(
                sourcePort: sourcePort,
                destinationPort: destinationPort,
                isSYN: flags & 0x02 != 0,
                isACK: flags & 0x10 != 0,
                isRST: flags & 0x04 != 0,
                payloadBytes: payloadBytes
            )
        }
    }
}
