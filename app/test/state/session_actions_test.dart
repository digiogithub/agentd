import 'package:agentd_app/state/session_actions.dart';
import 'package:flutter_riverpod/flutter_riverpod.dart';
import 'package:flutter_test/flutter_test.dart';

import 'launch_support.dart';

void main() {
  late FakeClient c;
  late ProviderContainer ct;
  setUp(() {
    c = FakeClient();
    ct = ProviderContainer(
      overrides: [
        clients({'h1': c}),
      ],
    );
    addTearDown(ct.dispose);
  });
  SessionActions a() => ct.read(sessionActionsProvider);

  test('kill defaults to TERM and supports KILL', () async {
    expect(await a().kill('h1', 's'), isA<ActionOk>());
    await a().kill('h1', 's', signal: 'KILL');
    expect(c.killCalls, [('s', 'TERM'), ('s', 'KILL')]);
  });

  test('remove passes force', () async {
    await a().remove('h1', 's');
    await a().remove('h1', 's', force: true);
    expect(c.removeCalls, [('s', false), ('s', true)]);
  });

  test('failures are friendly', () async {
    c.removeError = apiErr(409, 'still_running');
    final r = await a().remove('h1', 's') as ActionFailed;
    expect(r.message, contains('still running'));
    c.killError = apiErr(403, 'forbidden');
    expect(
      (await a().kill('h1', 's') as ActionFailed).message,
      contains('not allowed'),
    );
    c.removeError = apiErr(404, 'not_found');
    expect((await a().remove('h1', 's') as ActionFailed).notFound, isTrue);
  });
}
