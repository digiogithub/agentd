import 'package:agentd_app/platform/secret_store.dart';
import 'package:agentd_app/repositories/host.dart';
import 'package:agentd_app/state/agents.dart';
import 'package:agentd_app/state/providers.dart';
import 'package:flutter_riverpod/flutter_riverpod.dart';
import 'package:flutter_test/flutter_test.dart';

import '../support/fakes.dart';

class _CountingSecrets extends MemorySecretStore {
  int reads = 0;
  @override
  Future<String?> read(String key) {
    reads++;
    return super.read(key);
  }
}

Host host({
  String label = 'box',
  String url = 'https://box.example:7477',
  String device = 'dev1',
  String? pin,
}) => Host(
  id: 'h1',
  label: label,
  baseUrl: Uri.parse(url),
  deviceId: device,
  scope: 'write',
  hostname: 'box.local',
  addedAt: DateTime.utc(2026, 11, 1),
  pin: pin,
);

void main() {
  late TestEnv env;
  late _CountingSecrets secrets;
  late ProviderContainer c;

  setUp(() async {
    env = TestEnv();
    secrets = _CountingSecrets();
    c = ProviderContainer(
      // ignore: argument_type_not_assignable
      overrides: [
        appSupportDirProvider.overrideWith((ref) async => env.dir),
        secretStoreProvider.overrideWithValue(secrets),
      ].cast(),
    );
    addTearDown(c.dispose);
    await c.read(hostsProvider.future);
    await c.read(hostsProvider.notifier).add(host(), 'tok1');
    c.listen(hostClientProvider('h1'), (_, _) {});
    secrets.reads = 0;
  });
  tearDown(() => env.dispose());

  test('unrelated host list changes keep the very same client', () async {
    final c1 = await c.read(hostClientProvider('h1').future);
    await c.read(hostsProvider.notifier).rename('h1', 'renamed');
    await c.read(hostsProvider.notifier).markOk('h1');
    await c
        .read(hostsProvider.notifier)
        .add(
          Host(
            id: 'h2',
            label: 'other',
            baseUrl: Uri.parse('https://other.example:7477'),
            deviceId: 'dev9',
            scope: 'read',
            hostname: 'o',
            addedAt: DateTime.utc(2026, 11, 2),
          ),
          'tok2',
        );
    final c2 = await c.read(hostClientProvider('h1').future);
    expect(identical(c1, c2), isTrue);
    // The token was read from the secret store once, not per list change.
    expect(secrets.reads, 1);
  });

  test('re-pair, a new URL or a new pin rebuild the client', () async {
    final first = await c.read(hostClientProvider('h1').future);
    await c.read(hostsProvider.notifier).add(host(device: 'dev2'), 'tok2');
    final second = await c.read(hostClientProvider('h1').future);
    expect(identical(first, second), isFalse);

    await c
        .read(hostsProvider.notifier)
        .add(host(device: 'dev2', url: 'https://moved.example:7477'), 'tok2');
    final third = await c.read(hostClientProvider('h1').future);
    expect(identical(second, third), isFalse);
    expect(third.baseUrl.host, 'moved.example');

    await c
        .read(hostsProvider.notifier)
        .add(
          host(
            device: 'dev2',
            url: 'https://moved.example:7477',
            pin: 'sha256:${'ab' * 32}',
          ),
          'tok2',
        );
    final fourth = await c.read(hostClientProvider('h1').future);
    expect(identical(third, fourth), isFalse);
    expect(fourth.pin, isNotNull);
  });

  test('the agents feed shares the host client and does not own it', () async {
    final p = Provider((ref) => defaultHostFeed(ref, host()));
    final feed = await c.read(p) as ClientHostFeed;
    final shared = await c.read(hostClientProvider('h1').future);
    expect(identical(feed.client, shared), isTrue);
    expect(feed.ownsClient, isFalse);
  });
}
