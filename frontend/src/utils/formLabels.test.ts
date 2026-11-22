import { afterEach, describe, expect, it } from 'vitest';
import { associateUnlinkedFormLabels } from './formLabels';

function mount(html: string): HTMLElement {
  const host = document.createElement('div');
  host.innerHTML = html;
  document.body.append(host);
  return host;
}

describe('associateUnlinkedFormLabels', () => {
  afterEach(() => {
    document.body.innerHTML = '';
  });

  it('links a label to the control that follows it', () => {
    const host = mount('<div><label>Name</label><input></div>');
    associateUnlinkedFormLabels(host);
    const input = host.querySelector('input')!;
    expect(input.id).not.toBe('');
    expect(host.querySelector('label')!.htmlFor).toBe(input.id);
  });

  it('gives each label in a shared parent its own control', () => {
    const host = mount('<div><label>First</label><input><label>Second</label><input><label>Third</label><select></select></div>');
    associateUnlinkedFormLabels(host);
    const labels = Array.from(host.querySelectorAll('label'));
    const controls = Array.from(host.querySelectorAll('input, select'));
    labels.forEach((label, index) => expect(label.control).toBe(controls[index]));
    expect(new Set(controls.map((control) => control.id)).size).toBe(3);
  });

  it('links checkbox-then-label markup to the control before the label', () => {
    const host = mount('<div><input type="checkbox"><label>Remember me</label></div>');
    associateUnlinkedFormLabels(host);
    expect(host.querySelector('label')!.control).toBe(host.querySelector('input'));
  });

  it('does not take a control that already has a label', () => {
    const host = mount('<div><label for="owned">Owned</label><input id="owned"><label>Orphan</label></div>');
    associateUnlinkedFormLabels(host);
    const orphan = host.querySelectorAll('label')[1];
    expect(orphan.htmlFor).toBe('');
    expect(host.querySelector('label')!.control).toBe(host.querySelector('#owned'));
  });

  it('leaves labels that wrap their control, labels with for, and labels in groups alone', () => {
    const host = mount(`
      <div><label>Wrapped <input id="wrapped"></label></div>
      <div><label for="explicit">Explicit</label><input id="explicit"></div>
      <div role="radiogroup"><label>Option</label><input type="radio"></div>`);
    associateUnlinkedFormLabels(host);
    expect(host.querySelector('#wrapped')!.id).toBe('wrapped');
    expect(host.querySelector<HTMLLabelElement>('[role="radiogroup"] label')!.htmlFor).toBe('');
    expect(host.querySelector('[role="radiogroup"] input')!.id).toBe('');
  });

  it('does nothing when no control is available, and ignores hidden inputs', () => {
    const host = mount('<div><label>Lonely</label><input type="hidden"></div>');
    associateUnlinkedFormLabels(host);
    expect(host.querySelector('label')!.htmlFor).toBe('');
  });

  it('accepts a single label as the root', () => {
    const host = mount('<div><label>Late</label><input></div>');
    associateUnlinkedFormLabels(host.querySelector('label')!);
    expect(host.querySelector('label')!.control).toBe(host.querySelector('input'));
  });
});
