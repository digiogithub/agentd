/// Launch-agent state (frontend-app.md section 1.2, screen 4). See README.md.
library;

import 'dart:convert';

import 'package:flutter_riverpod/flutter_riverpod.dart';
import 'package:uuid/uuid.dart';

import '../api/agentd_api.dart';
import 'providers.dart';

/// Keeps one `Idempotency-Key` per logical write: while the request payload
/// is unchanged a retry reuses the key (the gateway replays the stored
/// answer instead of running the action twice); a changed payload gets a new
/// key and [clear] drops it after a definite answer.
class IdempotencyKeys {
  IdempotencyKeys({Uuid? uuid}) : _uuid = uuid ?? const Uuid();
  final Uuid _uuid;
  final _keys = <String, ({String sig, String key})>{};

  String keyFor(String scope, String payloadSig) {
    final cur = _keys[scope];
    if (cur != null && cur.sig == payloadSig) return cur.key;
    final key = _uuid.v4();
    _keys[scope] = (sig: payloadSig, key: key);
    return key;
  }

  void clear(String scope) => _keys.remove(scope);
}

/// Clock seam for the "started after the request" check of [checkOutcome].
final launchClockProvider = Provider<DateTime Function()>(
  (ref) => DateTime.now,
);

/// `GET /v1/info` of a host: aliases, harnesses, scopes.
final hostInfoProvider = FutureProvider.autoDispose.family<Info, String>(
  (ref, hostId) async =>
      (await ref.watch(hostClientProvider(hostId).future)).info(),
);

const _genericBasenames = {'devops', 'app', 'src', 'repo'};

/// Mirror of the daemon's `ResolveTarget` label rule, used only for the
/// default-name preview (the daemon decides the real name).
String projectLabel(String target, List<AliasInfo> aliases) {
  for (final a in aliases) {
    if (a.name == target) return a.name;
  }
  final clean = target.length > 1 && target.endsWith('/')
      ? target.substring(0, target.length - 1)
      : target;
  for (final a in aliases) {
    if (a.dir == clean) return a.name;
  }
  final parts = clean.split('/').where((p) => p.isNotEmpty).toList();
  if (parts.isEmpty) return '';
  final base = parts.last;
  if (_genericBasenames.contains(base) && parts.length > 1) {
    return '${parts[parts.length - 2]}-$base';
  }
  return base;
}

/// `<harness>@<label>`, or '' when it cannot be computed yet.
String defaultSessionName(
  String harness,
  String target,
  List<AliasInfo> aliases,
) {
  final label = projectLabel(target.trim(), aliases);
  if (harness.isEmpty || label.isEmpty) return '';
  return '$harness@$label';
}

/// Session names: `[a-zA-Z0-9._@-]+`.
final sessionNameRe = RegExp(r'^[a-zA-Z0-9._@-]+$');

enum LaunchPhase { idle, launching, success, failed, outcomeUnknown }

enum LaunchErrorKind {
  exists,
  forbidden,
  invalidTarget,
  starting,
  inProgress,
  auth,
  network,
  other,
}

class LaunchFailure {
  const LaunchFailure(this.kind, this.message);
  final LaunchErrorKind kind;
  final String message;
}

class LaunchState {
  const LaunchState({
    this.phase = LaunchPhase.idle,
    this.failure,
    this.session,
    this.expectedName = '',
  });
  final LaunchPhase phase;
  final LaunchFailure? failure;

  /// The created (or found after `outcome_unknown`) session.
  final SessionSummary? session;

  /// Name the user expects, used to look for the session after a lost answer.
  final String expectedName;
  bool get busy => phase == LaunchPhase.launching;
}

/// Friendly text for a failed `POST /v1/sessions`.
LaunchFailure mapLaunchError(Object e) {
  if (e is PinMismatchException) {
    return const LaunchFailure(
      LaunchErrorKind.network,
      'The host certificate changed. Re-pair the host.',
    );
  }
  if (e is ConnectionException) {
    return const LaunchFailure(
      LaunchErrorKind.network,
      'Cannot reach the host. Check the connection and try again.',
    );
  }
  if (e is ApiException) {
    switch (e.code) {
      case 'already_exists':
        return const LaunchFailure(
          LaunchErrorKind.exists,
          'A session with this name already exists. Pick another name.',
        );
      case 'forbidden':
        return LaunchFailure(
          LaunchErrorKind.forbidden,
          'This device is not allowed to launch that harness or use these '
          'options on this host.',
        );
      case 'invalid_argument':
      case 'unknown_alias':
      case 'not_a_directory':
      case 'unknown_profile':
        return LaunchFailure(
          LaunchErrorKind.invalidTarget,
          e.code == 'unknown_profile'
              ? 'Unknown profile for this harness.'
              : 'Invalid project: use an alias of the host or an absolute '
                    'directory that exists on it.',
        );
      case 'starting':
        return const LaunchFailure(
          LaunchErrorKind.starting,
          'That session name is still starting. Wait a moment.',
        );
      case 'in_progress':
        return const LaunchFailure(
          LaunchErrorKind.inProgress,
          'This launch is already in progress.',
        );
      case 'rate_limited':
        return const LaunchFailure(
          LaunchErrorKind.other,
          'Too many requests. Wait a moment and try again.',
        );
    }
    if (e.isUnauthenticated) {
      return const LaunchFailure(
        LaunchErrorKind.auth,
        'This device is no longer authorized. Re-pair the host.',
      );
    }
    if (e.isForbidden) {
      return const LaunchFailure(
        LaunchErrorKind.forbidden,
        'This device is not allowed to launch agents on this host.',
      );
    }
    return LaunchFailure(
      LaunchErrorKind.other,
      e.message.isEmpty ? 'Launch failed (${e.code}).' : e.message,
    );
  }
  return LaunchFailure(LaunchErrorKind.other, 'Launch failed: $e');
}

/// Runs a launch for one host. On `outcome_unknown` it does NOT retry: the
/// UI calls [checkOutcome] to look at the session list (the key is kept, so a
/// deliberate retry with the same form is still deduplicated by the gateway).
class LaunchController extends Notifier<LaunchState> {
  LaunchController(this.hostId);
  final String hostId;
  final _keys = IdempotencyKeys();
  RunRequest? _lastReq;
  DateTime? _requestedAt;

  @override
  LaunchState build() => const LaunchState();

  Future<AgentdClient> get _client =>
      ref.read(hostClientProvider(hostId).future);

  /// [expectedName] is the explicit or default name shown to the user.
  Future<void> launch(RunRequest req, {String expectedName = ''}) async {
    if (state.busy) return;
    _lastReq = req;
    _requestedAt = ref.read(launchClockProvider)();
    state = LaunchState(
      phase: LaunchPhase.launching,
      expectedName: expectedName,
    );
    final key = _keys.keyFor('run', jsonEncode(req.toJson()));
    try {
      final s = await (await _client).runSession(req, idempotencyKey: key);
      _keys.clear('run');
      state = LaunchState(
        phase: LaunchPhase.success,
        session: s,
        expectedName: expectedName,
      );
    } on ApiException catch (e) {
      if (e.isOutcomeUnknown) {
        state = LaunchState(
          phase: LaunchPhase.outcomeUnknown,
          expectedName: expectedName,
          failure: const LaunchFailure(
            LaunchErrorKind.network,
            'The connection dropped before the host answered. The agent may '
            'have started: refresh the session list instead of retrying.',
          ),
        );
        return;
      }
      if (e.code != 'in_progress' && !e.retryable) _keys.clear('run');
      state = LaunchState(
        phase: LaunchPhase.failed,
        failure: mapLaunchError(e),
        expectedName: expectedName,
      );
    } on Object catch (e) {
      state = LaunchState(
        phase: LaunchPhase.failed,
        failure: mapLaunchError(e),
        expectedName: expectedName,
      );
    }
  }

  /// After `outcome_unknown`: looks at the session list. The launch counts
  /// as done only when a session matches ALL of: the expected name, the
  /// requested harness (profile), the requested project (alias directory or
  /// the same path) and `started_at` not before the request was made. Any
  /// other session with that name (an older one, another project) is not ours.
  /// When nothing matches, the key is KEPT (a retry of the same form stays
  /// deduplicated by the gateway) and the state stays "outcome unknown": the
  /// UI says "not visible yet" and the user checks again; nothing is cleared
  /// or relaunched automatically. Returns true when found.
  Future<bool> checkOutcome() async {
    final name = state.expectedName;
    final req = _lastReq;
    try {
      final client = await _client;
      final list = await client.listSessions();
      var aliases = const <AliasInfo>[];
      try {
        aliases = (await client.info()).aliases;
      } on Object {
        // Without aliases only an exact path can match.
      }
      for (final s in list.sessions) {
        if (name.isNotEmpty && s.name == name && _isOurs(s, req, aliases)) {
          _keys.clear('run');
          state = LaunchState(
            phase: LaunchPhase.success,
            session: s,
            expectedName: name,
          );
          return true;
        }
      }
      state = LaunchState(
        phase: LaunchPhase.outcomeUnknown,
        expectedName: name,
        failure: const LaunchFailure(
          LaunchErrorKind.other,
          'Not visible yet: the agent did not show up on the host. Check '
          'again in a moment.',
        ),
      );
      return false;
    } on Object catch (e) {
      state = LaunchState(
        phase: LaunchPhase.outcomeUnknown,
        expectedName: name,
        failure: mapLaunchError(e),
      );
      return false;
    }
  }

  bool _isOurs(SessionSummary s, RunRequest? req, List<AliasInfo> aliases) {
    if (req == null) return false;
    final started = s.startedAt;
    final at = _requestedAt;
    if (started == null || at == null || started.isBefore(at)) return false;
    final harness = req.harness;
    if (harness != null && harness.isNotEmpty && s.profile != harness) {
      return false;
    }
    final dir = _targetDir(req.target, aliases);
    return dir != null && _trimSlash(dir) == _trimSlash(s.cwd);
  }

  static String _trimSlash(String p) =>
      p.length > 1 && p.endsWith('/') ? p.substring(0, p.length - 1) : p;

  /// Directory the target stands for: an alias's dir, or the target itself
  /// when it is an absolute path.
  static String? _targetDir(String target, List<AliasInfo> aliases) {
    final t = target.trim();
    for (final a in aliases) {
      if (a.name == t) return a.dir;
    }
    return t.startsWith('/') ? t : null;
  }

  void reset() => state = const LaunchState();
}

final launchControllerProvider = NotifierProvider.autoDispose
    .family<LaunchController, LaunchState, String>(LaunchController.new);
