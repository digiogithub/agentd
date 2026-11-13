import 'dart:io';

import 'package:agentd_app/platform/secret_store.dart';
import 'package:agentd_app/repositories/host.dart';
import 'package:agentd_app/repositories/host_store.dart';
import 'package:flutter_test/flutter_test.dart';

import '../support/fakes.dart';

void main() {
  late TestEnv env;
  setUp(() => env = TestEnv());
  tearDown(() => env.dispose());

  test(
    'token lives only in the secret store, never in the JSON file',
    () async {
      const token = 'tok_SUPERSECRET_1234567890';
      await env.store.add(sampleHost(), token);
      final raw = File('${env.dir.path}/hosts.json').readAsStringSync();
      expect(raw, isNot(contains(token)));
      expect(raw, isNot(contains('SUPERSECRET')));
      expect(raw, contains('dev_h1'));
      expect(env.secrets.data['token/h1'], token);
      expect(await env.store.token('h1'), token);
      for (final f in env.dir.listSync(recursive: true).whereType<File>()) {
        expect(f.readAsStringSync(), isNot(contains(token)));
      }
    },
  );

  test('round trips hosts and replaces by id', () async {
    final s = env.store;
    await s.add(sampleHost(pin: 'sha256:${'cd' * 32}'), 't1');
    await s.add(sampleHost(id: 'h2', label: 'two'), 't2');
    await s.add(
      sampleHost(label: 'renamed', pin: 'sha256:${'cd' * 32}'),
      't1b',
    );
    final hosts = await env.store.list();
    expect(hosts.map((h) => h.id), ['h1', 'h2']);
    expect(hosts.first.label, 'renamed');
    expect(hosts.first.pin, 'sha256:${'cd' * 32}');
    expect(hosts.first.addedAt, DateTime.utc(2026, 11, 1));
    expect(await s.token('h1'), 't1b');
  });

  test('update and remove (token deleted too)', () async {
    final s = env.store;
    await s.add(sampleHost(), 't1');
    await s.update(
      sampleHost().copyWith(label: 'new', lastOk: DateTime.utc(2026)),
    );
    expect((await s.list()).single.label, 'new');
    expect((await s.list()).single.lastOk, DateTime.utc(2026));
    await s.remove('h1');
    expect(await s.list(), isEmpty);
    expect(await s.token('h1'), isNull);
  });

  test('refusing secret store stores nothing', () async {
    env.secrets.unavailable = true;
    await expectLater(
      env.store.add(sampleHost(), 't'),
      throwsA(isA<SecretStoreUnavailable>()),
    );
    expect(File('${env.dir.path}/hosts.json').existsSync(), isFalse);
  });

  test('corrupt file yields an empty list', () async {
    File('${env.dir.path}/hosts.json').writeAsStringSync('{nope');
    expect(await env.store.list(), isEmpty);
  });

  test('no temp file left behind', () async {
    await env.store.add(sampleHost(), 't');
    expect(File('${env.dir.path}/hosts.json.tmp').existsSync(), isFalse);
  });

  group('write ordering and failure injection', () {
    Host h({String id = 'h1', String dev = 'dev'}) => Host(
      id: id,
      label: 'l$id',
      baseUrl: Uri.parse('https://box.example:7477'),
      deviceId: dev,
      scope: 'write',
      hostname: 'box',
      addedAt: DateTime.utc(2026, 11, 1),
    );

    List<String> files() =>
        env.dir.listSync().map((e) => e.path.split('/').last).toList()..sort();

    test('a refusing secret store leaves no host and no temp file', () async {
      env.secrets.unavailable = true;
      await expectLater(
        env.store.add(h(), 'tok'),
        throwsA(isA<SecretStoreUnavailable>()),
      );
      env.secrets.unavailable = false;
      expect(await env.store.list(), isEmpty);
      expect(files(), isNot(contains('hosts.json.tmp')));
      expect(env.secrets.data, isEmpty);
    });

    test('a failed commit rolls the new token back (add)', () async {
      final store = HostStore(
        dir: env.dir,
        secrets: env.secrets,
        rename: (from, to) async => throw const FileSystemException('disk'),
      );
      await expectLater(
        store.add(h(), 'tok'),
        throwsA(isA<FileSystemException>()),
      );
      expect(env.secrets.data, isEmpty);
      expect(await env.store.list(), isEmpty);
      expect(files(), isNot(contains('hosts.json.tmp')));
    });

    test('a failed commit restores the previous token (re-pair)', () async {
      await env.store.add(h(), 'old-token');
      final store = HostStore(
        dir: env.dir,
        secrets: env.secrets,
        rename: (from, to) async => throw const FileSystemException('disk'),
      );
      await expectLater(
        store.add(h(dev: 'dev2'), 'new-token'),
        throwsA(isA<FileSystemException>()),
      );
      expect(env.secrets.data['token/h1'], 'old-token');
      final hosts = await env.store.list();
      expect(hosts.single.deviceId, 'dev');
      expect(files(), isNot(contains('hosts.json.tmp')));
    });

    test('the token is written between staging and commit', () async {
      final order = <String>[];
      final secrets = _OrderedSecrets(order);
      final store = HostStore(
        dir: env.dir,
        secrets: secrets,
        rename: (from, to) async {
          order.add('commit');
          expect(from.existsSync(), isTrue, reason: 'staged before commit');
          await from.rename(to.path);
        },
      );
      await store.add(h(), 'tok');
      expect(order, ['write token/h1', 'commit']);
    });

    test('remove deletes the token first; a failure leaves the host', () async {
      await env.store.add(h(), 'tok');
      env.secrets.unavailable = true;
      await expectLater(
        env.store.remove('h1'),
        throwsA(isA<SecretStoreUnavailable>()),
      );
      env.secrets.unavailable = false;
      // Still listed with its token: consistent, nothing orphaned.
      expect((await env.store.list()).single.id, 'h1');
      expect(env.secrets.data['token/h1'], 'tok');

      // Metadata save failing after the token is gone: no orphan secret.
      final store = HostStore(
        dir: env.dir,
        secrets: env.secrets,
        rename: (from, to) async => throw const FileSystemException('disk'),
      );
      await expectLater(
        store.remove('h1'),
        throwsA(isA<FileSystemException>()),
      );
      expect(env.secrets.data, isEmpty);
      expect(await env.store.token('h1'), isNull);
    });
  });
}

class _OrderedSecrets extends MemorySecretStore {
  _OrderedSecrets(this.order);
  final List<String> order;
  @override
  Future<void> write(String key, String value) async {
    order.add('write $key');
    await super.write(key, value);
  }
}
