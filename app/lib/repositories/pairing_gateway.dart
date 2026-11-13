/// Seam between the pairing flow and [AgentdClient] (faked in tests).
library;

import '../api/agentd_api.dart';

abstract class PairingGateway {
  Future<PairResult> pair({
    required String code,
    required String name,
    String? platform,
  });
  Future<bool> health();
  Future<Info> info();

  /// Aborts a pending request.
  void close();
}

typedef GatewayFactory = PairingGateway Function(
  Uri baseUrl,
  SpkiPin? pin,
  String token,
);

class ClientGateway implements PairingGateway {
  ClientGateway(Uri baseUrl, SpkiPin? pin, String token)
    : _c = AgentdClient(baseUrl: baseUrl, pin: pin, token: token);
  final AgentdClient _c;

  @override
  Future<PairResult> pair({
    required String code,
    required String name,
    String? platform,
  }) => _c.pair(code: code, name: name, platform: platform);

  @override
  Future<bool> health() => _c.health();

  @override
  Future<Info> info() => _c.info();

  @override
  void close() => _c.close();
}

PairingGateway defaultGatewayFactory(Uri baseUrl, SpkiPin? pin, String token) =>
    ClientGateway(baseUrl, pin, token);
