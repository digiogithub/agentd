/// Secret storage for device tokens (frontend-app.md section 5.1).
library;

import 'dart:io' show Platform;

import 'package:flutter/services.dart';
import 'package:flutter_secure_storage/flutter_secure_storage.dart';

/// Secure storage cannot be used (e.g. Linux without a Secret Service). The
/// token is never stored elsewhere instead.
class SecretStoreUnavailable implements Exception {
  SecretStoreUnavailable(this.message);
  final String message;
  @override
  String toString() => 'SecretStoreUnavailable($message)';
}

/// Explanation shown when the secret store fails. On Android a failure is
/// almost always a keystore key that can no longer unwrap the saved token
/// (device reset, restore, security change): the fix is to pair again. The
/// libsecret advice only applies to Linux.
String secretStoreHint({required bool android}) => android
    ? 'The saved device token could not be unlocked on this phone (the '
          'Android keystore key was reset or changed). Pair the host again.'
    : 'No secret service is available to keep the device token safely. '
          'On Linux install and start gnome-keyring, KWallet or KeePassXC '
          '(libsecret), unlock it, then try again. agentd will not store the '
          'token in a plain file.';

abstract class SecretStore {
  Future<String?> read(String key);
  Future<void> write(String key, String value);
  Future<void> delete(String key);
}

class FlutterSecretStore implements SecretStore {
  FlutterSecretStore([FlutterSecureStorage? storage])
    : _s =
          storage ??
          const FlutterSecureStorage(
            mOptions: MacOsOptions(usesDataProtectionKeychain: false),
          );
  final FlutterSecureStorage _s;

  Future<T> _guard<T>(Future<T> Function() f) async {
    final android = Platform.isAndroid;
    try {
      return await f();
    } on PlatformException catch (e) {
      throw SecretStoreUnavailable(
        '${secretStoreHint(android: android)} '
        '(${e.code})',
      );
    } on MissingPluginException {
      throw SecretStoreUnavailable(secretStoreHint(android: android));
    }
  }

  @override
  Future<String?> read(String key) => _guard(() => _s.read(key: key));

  @override
  Future<void> write(String key, String value) =>
      _guard(() => _s.write(key: key, value: value));

  @override
  Future<void> delete(String key) => _guard(() => _s.delete(key: key));
}

/// In-memory store for tests.
class MemorySecretStore implements SecretStore {
  final Map<String, String> data = {};

  /// When set, every call throws [SecretStoreUnavailable].
  bool unavailable = false;

  void _check() {
    if (unavailable) throw SecretStoreUnavailable('no secret service');
  }

  @override
  Future<String?> read(String key) async {
    _check();
    return data[key];
  }

  @override
  Future<void> write(String key, String value) async {
    _check();
    data[key] = value;
  }

  @override
  Future<void> delete(String key) async {
    _check();
    data.remove(key);
  }
}
