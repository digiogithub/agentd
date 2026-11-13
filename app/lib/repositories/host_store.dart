/// Host metadata (JSON file, no secrets) plus tokens in a [SecretStore].
library;

import 'dart:convert';
import 'dart:io';

import 'package:flutter/foundation.dart' show visibleForTesting;

import '../platform/secret_store.dart';
import 'host.dart';

class HostStore {
  HostStore({
    required this.dir,
    required this._secrets,
    @visibleForTesting Future<void> Function(File from, File to)? rename,
  }) : _rename = rename ?? ((from, to) => from.rename(to.path));

  final Directory dir;
  final Future<void> Function(File from, File to) _rename;
  final SecretStore _secrets;
  Future<void> _lock = Future.value();

  File get _file => File('${dir.path}/hosts.json');

  static String tokenKey(String hostId) => 'token/$hostId';

  Future<T> _serial<T>(Future<T> Function() f) {
    final r = _lock.then((_) => f());
    _lock = r.then((_) {}, onError: (_) {});
    return r;
  }

  Future<List<Host>> _load() async {
    if (!await _file.exists()) return [];
    try {
      final j = jsonDecode(await _file.readAsString());
      if (j is! Map || j['hosts'] is! List) return [];
      return [
        for (final h in j['hosts'] as List<Object?>)
          if (h is Map<String, Object?>) Host.fromJson(h),
      ];
    } on Object {
      // A corrupt file must not brick the app; hosts can be re-paired.
      return [];
    }
  }

  /// Writes the whole list to the temp file (not yet visible).
  Future<File> _stage(List<Host> hosts) async {
    await dir.create(recursive: true);
    final tmp = File('${_file.path}.tmp');
    await tmp.writeAsString(
      jsonEncode({
        'version': 1,
        'hosts': [for (final h in hosts) h.toJson()],
      }),
      flush: true,
    );
    return tmp;
  }

  /// Atomic commit of a staged file: rename over `hosts.json`.
  Future<void> _commit(File tmp) => _rename(tmp, _file);

  Future<void> _discard(File tmp) async {
    try {
      if (await tmp.exists()) await tmp.delete();
    } on Object {
      // Best effort: the next _stage overwrites it.
    }
  }

  /// Atomic write: temp file in the same directory, then rename.
  Future<void> _save(List<Host> hosts) async => _commit(await _stage(hosts));

  Future<List<Host>> list() => _serial(_load);

  /// Transactional add / re-pair: the new metadata is staged in a temp file,
  /// then the token is written, then the temp file is committed (renamed).
  /// A failure at any step rolls back what was already done: a refusing
  /// secret store leaves no record, and a failed commit puts the previous
  /// token back (or removes the new one), so a re-pair never leaves a host
  /// with a token that belongs to another device. An existing host with the
  /// same id is replaced.
  Future<void> add(Host host, String token) => _serial(() async {
    final key = tokenKey(host.id);
    final hosts = await _load();
    final i = hosts.indexWhere((h) => h.id == host.id);
    final previous = i >= 0 ? await _secrets.read(key) : null;
    if (i >= 0) {
      hosts[i] = host;
    } else {
      hosts.add(host);
    }
    final tmp = await _stage(hosts);
    try {
      await _secrets.write(key, token);
    } on Object {
      await _discard(tmp);
      rethrow;
    }
    try {
      await _commit(tmp);
    } on Object {
      await _discard(tmp);
      try {
        if (previous != null) {
          await _secrets.write(key, previous);
        } else {
          await _secrets.delete(key);
        }
      } on Object {
        // Nothing more can be done; the host record was never committed.
      }
      rethrow;
    }
  });

  Future<void> update(Host host) => _serial(() async {
    final hosts = await _load();
    final i = hosts.indexWhere((h) => h.id == host.id);
    if (i < 0) return;
    hosts[i] = host;
    await _save(hosts);
  });

  /// Token first, then metadata: a failure in between leaves a host without
  /// a token ("re-pair"), never a token without a host (an orphan secret).
  Future<void> remove(String id) => _serial(() async {
    await _secrets.delete(tokenKey(id));
    final hosts = await _load();
    hosts.removeWhere((h) => h.id == id);
    await _save(hosts);
  });

  Future<String?> token(String hostId) => _secrets.read(tokenKey(hostId));
}
