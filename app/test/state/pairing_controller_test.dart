import 'package:flutter_riverpod/flutter_riverpod.dart';

import 'dart:async';

import 'package:agentd_app/api/agentd_api.dart';
import 'package:agentd_app/repositories/pair_link.dart';
import 'package:agentd_app/state/pairing_controller.dart';
import 'package:agentd_app/state/providers.dart';
import 'package:flutter_test/flutter_test.dart';

import '../support/fakes.dart';

void main() {
  late TestEnv env;
  late ProviderContainer c;
  final link = PairLink.parse(
    'agentd://pair?v=1&u=https%3A%2F%2Fbox.example%3A7477&c=ABCD-EFGH-JKMN&h=box',
  );

  PairingState st() => c.read(pairingControllerProvider);

  setUp(() {
    env = TestEnv();
    c = container(env);
    c.listen(pairingControllerProvider, (_, _) {});
    _busy = () =>
        st().phase == PairPhase.waiting && env.gateways.pairResult.isCompleted;
  });
  tearDown(() {
    c.dispose();
    env.dispose();
  });

  Future<void> start({String? replace}) async {
    await c.read(hostsProvider.future);
    unawaited(
      c
          .read(pairingControllerProvider.notifier)
          .start(link, deviceName: 'my laptop', replaceHostId: replace),
    );
    await Future<void>.delayed(Duration.zero);
  }

  test('success saves host and token, runs health+info', () async {
    await start();
    expect(st().phase, PairPhase.waiting);
    expect(env.gateways.pairCalls.single.code, 'ABCD-EFGH-JKMN');
    expect(env.gateways.pairCalls.single.name, 'my laptop');
    env.gateways.infoHost = 'realhost';
    env.gateways.pairResult.complete(
      const PairResult(token: 'tok1', deviceId: 'dev9', scope: Scope.write),
    );
    await pumpEvents();
    expect(st().phase, PairPhase.success);
    final host = st().host!;
    expect(host.deviceId, 'dev9');
    expect(host.scope, 'write');
    expect(host.hostname, 'realhost');
    expect(host.label, 'realhost');
    expect(env.secrets.data['token/${host.id}'], 'tok1');
    expect((await c.read(hostsProvider.future)).length, 1);
    expect(env.gateways.created.every((g) => g.closed), isTrue);
  });

  test('first check failure is a warning, host still saved', () async {
    env.gateways.healthError = ConnectionException('down');
    await start();
    env.gateways.pairResult.complete(
      const PairResult(token: 't', deviceId: 'd'),
    );
    await pumpEvents();
    expect(st().phase, PairPhase.success);
    expect(st().warning, isNotNull);
    expect(st().host!.lastOk, isNull);
  });

  Future<void> failWith(Object e) async {
    await start();
    env.gateways.pairResult.completeError(e);
    await pumpEvents();
  }

  test('denied', () async {
    await failWith(
      ApiException(statusCode: 403, code: 'pairing_denied', message: 'x'),
    );
    expect(st().phase, PairPhase.failed);
    expect(st().failure!.kind, PairFailureKind.denied);
    expect(env.secrets.data, isEmpty);
  });

  test('expired', () async {
    await failWith(
      ApiException(statusCode: 410, code: 'pairing_expired', message: 'x'),
    );
    expect(st().failure!.kind, PairFailureKind.expired);
  });

  test('invalid, rate limited, network, pin mismatch', () async {
    await failWith(
      ApiException(statusCode: 403, code: 'pairing_invalid', message: 'x'),
    );
    expect(st().failure!.kind, PairFailureKind.invalid);
    env.gateways.pairResult = Completer<PairResult>();
    await failWith(
      ApiException(
        statusCode: 429,
        code: 'rate_limited',
        message: 'x',
        retryAfter: const Duration(seconds: 30),
      ),
    );
    expect(st().failure!.kind, PairFailureKind.rateLimited);
    expect(st().failure!.message, contains('30'));
    env.gateways.pairResult = Completer<PairResult>();
    await failWith(ConnectionException('refused'));
    expect(st().failure!.kind, PairFailureKind.network);
    env.gateways.pairResult = Completer<PairResult>();
    await failWith(PinMismatchException(expected: 'a', seen: 'b'));
    expect(st().failure!.kind, PairFailureKind.pinMismatch);
  });

  test('cancel aborts and ignores a late result', () async {
    await start();
    c.read(pairingControllerProvider.notifier).cancel();
    expect(st().phase, PairPhase.cancelled);
    expect(env.gateways.created.first.closed, isTrue);
    env.gateways.pairResult.complete(
      const PairResult(token: 't', deviceId: 'd'),
    );
    await pumpEvents();
    expect(st().phase, PairPhase.cancelled);
    expect(env.secrets.data, isEmpty);
    expect(await c.read(hostsProvider.future), isEmpty);
  });

  test('refusing secret store surfaces a clear failure', () async {
    await start();
    env.secrets.unavailable = true;
    env.gateways.pairResult.complete(
      const PairResult(token: 't', deviceId: 'd'),
    );
    await pumpEvents();
    expect(st().phase, PairPhase.failed);
    expect(st().failure!.kind, PairFailureKind.secretStore);
  });

  group('re-pair keeps URL and pin unless the change is confirmed', () {
    final pin = 'sha256:${'ab' * 32}';
    final pinned = PairLink.parse(
      'agentd://pair?v=1&u=https%3A%2F%2Fbox.example%3A7477&c=ABCD-EFGH-JKMN'
      '&fp=$pin&h=box',
    );

    Future<void> seed({String? withPin}) async {
      await env.store.add(sampleHost(pin: withPin), 'old');
      c.invalidate(hostsProvider);
      await c.read(hostsProvider.future);
    }

    test('a link without the pin is refused, nothing is sent', () async {
      await seed(withPin: pin);
      await c
          .read(pairingControllerProvider.notifier)
          .start(link, replaceHostId: 'h1');
      expect(st().phase, PairPhase.failed);
      expect(st().failure!.kind, PairFailureKind.endpointChanged);
      expect(env.gateways.pairCalls, isEmpty);
      expect(env.gateways.created, isEmpty);
      final hosts = await c.read(hostsProvider.future);
      expect(hosts.single.pin, pin);
      expect(env.secrets.data['token/h1'], 'old');
    });

    test('another URL or another pin is refused too', () async {
      await seed(withPin: pin);
      final moved = PairLink.parse(
        'agentd://pair?v=1&u=https%3A%2F%2Fevil.example%3A7477'
        '&c=ABCD-EFGH-JKMN&fp=$pin',
      );
      await c
          .read(pairingControllerProvider.notifier)
          .start(moved, replaceHostId: 'h1');
      expect(st().failure!.kind, PairFailureKind.endpointChanged);
      final other = PairLink.parse(
        'agentd://pair?v=1&u=https%3A%2F%2Fbox.example%3A7477'
        '&c=ABCD-EFGH-JKMN&fp=sha256:${'cd' * 32}',
      );
      await c
          .read(pairingControllerProvider.notifier)
          .start(other, replaceHostId: 'h1');
      expect(st().failure!.kind, PairFailureKind.endpointChanged);
      expect(env.gateways.pairCalls, isEmpty);
    });

    test('the same URL and pin re-pair without asking', () async {
      await seed(withPin: pin);
      unawaited(
        c
            .read(pairingControllerProvider.notifier)
            .start(pinned, replaceHostId: 'h1'),
      );
      await Future<void>.delayed(Duration.zero);
      expect(st().phase, PairPhase.waiting);
      env.gateways.pairResult.complete(
        const PairResult(token: 'new', deviceId: 'dev2'),
      );
      await pumpEvents();
      expect(st().phase, PairPhase.success);
      expect((await c.read(hostsProvider.future)).single.pin, pin);
    });

    test('an explicit confirmation lets the change through', () async {
      await seed(withPin: pin);
      unawaited(
        c
            .read(pairingControllerProvider.notifier)
            .start(link, replaceHostId: 'h1', allowEndpointChange: true),
      );
      await Future<void>.delayed(Duration.zero);
      expect(st().phase, PairPhase.waiting);
      env.gateways.pairResult.complete(
        const PairResult(token: 'new', deviceId: 'dev2'),
      );
      await pumpEvents();
      expect((await c.read(hostsProvider.future)).single.pin, isNull);
    });
  });

  test('re-pair keeps id and label', () async {
    await env.store.add(sampleHost(label: 'mine'), 'old');
    c.invalidate(hostsProvider);
    await start(replace: 'h1');
    env.gateways.pairResult.complete(
      const PairResult(token: 'new', deviceId: 'dev2'),
    );
    await pumpEvents();
    final hosts = await c.read(hostsProvider.future);
    expect(hosts.length, 1);
    expect(hosts.single.id, 'h1');
    expect(hosts.single.label, 'mine');
    expect(hosts.single.deviceId, 'dev2');
    expect(env.secrets.data['token/h1'], 'new');
  });
}

Future<void> pumpEvents() async {
  // File IO is real async work: give it wall-clock time (generous under load).
  await Future<void>.delayed(const Duration(milliseconds: 100));
  for (var i = 0; i < 100 && _busy(); i++) {
    await Future<void>.delayed(const Duration(milliseconds: 50));
  }
}

bool Function() _busy = () => false;
