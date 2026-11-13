import 'package:agentd_app/api/agentd_api.dart';
import 'package:agentd_app/state/launch.dart';
import 'package:flutter_riverpod/flutter_riverpod.dart';
import 'package:flutter_test/flutter_test.dart';

import 'launch_support.dart';

void main() {
  late FakeClient c;
  late ProviderContainer ct;
  final t0 = DateTime.utc(2026, 11, 13, 10);
  setUp(() {
    c = FakeClient();
    ct = ProviderContainer(
      overrides: [
        clients({'h1': c}),
        launchClockProvider.overrideWithValue(() => t0),
      ],
    );
    addTearDown(ct.dispose);
    ct.listen(launchControllerProvider('h1'), (_, _) {});
  });
  LaunchController ctl() => ct.read(launchControllerProvider('h1').notifier);
  LaunchState st() => ct.read(launchControllerProvider('h1'));
  const req = RunRequest(target: 'webapp', harness: 'codex');

  test('labels and default names', () {
    const al = [AliasInfo(name: 'webapp', dir: '/src/webapp')];
    expect(defaultSessionName('codex', 'webapp', al), 'codex@webapp');
    expect(defaultSessionName('codex', '/src/webapp/', al), 'codex@webapp');
    expect(defaultSessionName('claude', '/x/y/proj', al), 'claude@proj');
    expect(defaultSessionName('claude', '/x/y/app', al), 'claude@y-app');
    expect(defaultSessionName('claude', '', al), '');
  });

  test('success', () async {
    await ctl().launch(req, expectedName: 'codex@webapp');
    expect(st().phase, LaunchPhase.success);
    expect(st().session!.name, 'codex@webapp');
    expect(c.runCalls.single.key, isNotEmpty);
  });

  for (final (code, status, kind) in [
    ('already_exists', 409, LaunchErrorKind.exists),
    ('forbidden', 403, LaunchErrorKind.forbidden),
    ('not_a_directory', 400, LaunchErrorKind.invalidTarget),
    ('unknown_alias', 400, LaunchErrorKind.invalidTarget),
  ]) {
    test('error $code', () async {
      c.runError = apiErr(status, code);
      await ctl().launch(req);
      expect(st().phase, LaunchPhase.failed);
      expect(st().failure!.kind, kind);
    });
  }

  SessionSummary ours({
    String name = 'codex@webapp',
    String cwd = '/home/jose/src/webapp',
    String profile = 'codex',
    DateTime? startedAt,
  }) => SessionSummary(
    name: name,
    cwd: cwd,
    profile: profile,
    startedAt: startedAt ?? t0.add(const Duration(seconds: 1)),
  );

  test('outcome_unknown: no retry, check finds the session', () async {
    c.runError = ApiException.outcomeUnknown('timeout');
    await ctl().launch(req, expectedName: 'codex@webapp');
    expect(st().phase, LaunchPhase.outcomeUnknown);
    expect(c.runCalls.length, 1);
    c.sessions = [ours()];
    expect(await ctl().checkOutcome(), isTrue);
    expect(st().phase, LaunchPhase.success);
    expect(c.runCalls.length, 1);
  });

  test('check never adopts a look-alike session', () async {
    c.runError = ApiException.outcomeUnknown('timeout');
    await ctl().launch(req, expectedName: 'codex@webapp');
    final lookAlikes = {
      'another project': ours(cwd: '/srv/api'),
      'another harness': ours(profile: 'claude'),
      'older than the request': ours(
        startedAt: t0.subtract(const Duration(minutes: 5)),
      ),
      'no start time': const SessionSummary(
        name: 'codex@webapp',
        cwd: '/home/jose/src/webapp',
        profile: 'codex',
      ),
      'another name': ours(name: 'codex@other'),
    };
    for (final e in lookAlikes.entries) {
      c.sessions = [e.value];
      expect(await ctl().checkOutcome(), isFalse, reason: e.key);
      expect(st().phase, LaunchPhase.outcomeUnknown, reason: e.key);
    }
    expect(c.runCalls.length, 1);
  });

  test('absent session: not visible yet, key kept, no auto clear', () async {
    c.runError = ApiException.outcomeUnknown('timeout');
    await ctl().launch(req, expectedName: 'codex@webapp');
    expect(await ctl().checkOutcome(), isFalse);
    expect(st().phase, LaunchPhase.outcomeUnknown);
    expect(st().failure!.message, contains('Not visible yet'));
    // Still not there on a second look; the user may check again later.
    expect(await ctl().checkOutcome(), isFalse);
    expect(st().phase, LaunchPhase.outcomeUnknown);
    // The same form relaunched is still deduplicated by the gateway.
    c.runError = null;
    ctl().reset();
    await ctl().launch(req, expectedName: 'codex@webapp');
    expect(c.runCalls[1].key, c.runCalls[0].key);
    // An absolute-path target matches by directory too.
    ctl().reset();
    c.runError = ApiException.outcomeUnknown('x');
    await ctl().launch(
      const RunRequest(target: '/srv/api/', harness: 'codex'),
      expectedName: 'codex@api',
    );
    c.sessions = [ours(name: 'codex@api', cwd: '/srv/api')];
    expect(await ctl().checkOutcome(), isTrue);
  });

  test(
    'same key on retry of identical request, new key when changed',
    () async {
      c.runError = ApiException.outcomeUnknown('x');
      await ctl().launch(req);
      c.runError = apiErr(503, 'daemon_unavailable');
      await ctl().launch(req);
      await ctl().launch(const RunRequest(target: 'api', harness: 'codex'));
      expect(c.runCalls[0].key, c.runCalls[1].key);
      expect(c.runCalls[2].key, isNot(c.runCalls[0].key));
    },
  );
}
