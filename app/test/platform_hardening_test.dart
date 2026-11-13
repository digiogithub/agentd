import 'dart:io';

import 'package:agentd_app/api/agentd_api.dart';
import 'package:agentd_app/api/loopback.dart';
import 'package:agentd_app/platform/secret_store.dart';
import 'package:agentd_app/repositories/pair_link.dart';
import 'package:flutter_test/flutter_test.dart';

void main() {
  group('10.0.2.2 is loopback in debug builds only', () {
    test('tests run as a debug build', () => expect(isDebugBuild, isTrue));

    test('release: the emulator alias is a plain remote address', () {
      expect(isLoopbackHost('10.0.2.2', allowEmulator: false), isFalse);
      expect(isLoopbackHost('10.0.2.2', allowEmulator: true), isTrue);
      expect(isLoopbackHost('10.0.2.2'), isDebugBuild);
      // Real loopback is never affected.
      expect(isLoopbackHost('localhost', allowEmulator: false), isTrue);
      expect(isLoopbackHost('127.0.0.1', allowEmulator: false), isTrue);
      expect(isLoopbackHost('::1', allowEmulator: false), isTrue);
      expect(isLoopbackHost('192.168.1.5', allowEmulator: true), isFalse);
    });

    test('pairing links: plain http to 10.0.2.2 needs a debug build', () {
      expect(
        () => parseBaseUrl('http://10.0.2.2:7477', allowEmulator: false),
        throwsA(isA<PairLinkException>()),
      );
      expect(
        parseBaseUrl('http://10.0.2.2:7477', allowEmulator: true).host,
        '10.0.2.2',
      );
      expect(parseBaseUrl('https://10.0.2.2:7477').scheme, 'https');
    });

    test('the client itself still accepts it here (debug)', () {
      expect(
        () => AgentdClient(
          baseUrl: Uri.parse('http://10.0.2.2:7477'),
          token: 't',
        ).close(),
        returnsNormally,
      );
    });
  });

  group('secret store hint', () {
    test('Android: re-pair, never "install gnome-keyring"', () {
      final m = secretStoreHint(android: true);
      expect(m.toLowerCase(), contains('pair'));
      expect(m.toLowerCase(), isNot(contains('keyring')));
      expect(m.toLowerCase(), isNot(contains('libsecret')));
    });

    test('desktop Linux keeps the keyring advice', () {
      final m = secretStoreHint(android: false);
      expect(m.toLowerCase(), contains('gnome-keyring'));
    });
  });

  group('android backup', () {
    final manifest = File('android/app/src/main/AndroidManifest.xml')
        .readAsStringSync();

    test('no backup of tokens or host data', () {
      expect(manifest, contains('android:allowBackup="false"'));
      expect(manifest, contains('android:fullBackupContent="false"'));
      expect(
        manifest,
        contains('android:dataExtractionRules="@xml/data_extraction_rules"'),
      );
    });

    test('data extraction rules exclude every domain, both channels', () {
      final xml = File('android/app/src/main/res/xml/data_extraction_rules.xml')
          .readAsStringSync();
      for (final section in ['cloud-backup', 'device-transfer']) {
        final body = RegExp('<$section>([\\s\\S]*?)</$section>')
            .firstMatch(xml)!
            .group(1)!;
        for (final d in [
          'root',
          'file',
          'database',
          'sharedpref',
          'external',
        ]) {
          expect(
            body,
            contains('<exclude domain="$d"/>'),
            reason: '$section $d',
          );
        }
        expect(body, isNot(contains('<include')));
      }
    });
  });
}
