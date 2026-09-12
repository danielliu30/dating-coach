import { fireEvent, render, screen, waitFor } from '@testing-library/react-native';
import React from 'react';

import { api } from '../api/client';
import SubmitConversationScreen, { parseTranscript } from './SubmitConversationScreen';

jest.mock('../api/client', () => ({ api: { submitConversation: jest.fn() } }));

const mocked = api as jest.Mocked<typeof api>;
const navigation = { navigate: jest.fn() };
const props = { navigation, route: { key: 'Submit', name: 'Submit' } } as unknown as React.ComponentProps<
  typeof SubmitConversationScreen
>;

const transcriptInput = () => screen.getByPlaceholderText(/^me: your profile says you bake/);

describe('parseTranscript', () => {
  it('maps speaker aliases and reports bad lines with their 1-based index', () => {
    const { messages, errors } = parseTranscript('me: hi\n\n  them: hey  \nYou: ok\nbob: ?\nno prefix');
    expect(messages).toEqual([
      { sender: 'self', body: 'hi' },
      { sender: 'match', body: 'hey' },
      { sender: 'self', body: 'ok' },
    ]);
    expect(errors).toEqual(['Line 4: unknown speaker "bob".', 'Line 5 is missing a "me:" or "them:" prefix.']);
  });
});

describe('SubmitConversationScreen', () => {
  it('refuses to submit with zero parsed messages', () => {
    render(<SubmitConversationScreen {...props} />);
    fireEvent.press(screen.getByText('Get feedback'));
    expect(screen.getByText('Paste at least one message.')).toBeTruthy();
    expect(mocked.submitConversation).not.toHaveBeenCalled();
  });

  it('surfaces the first parse error and does not submit', () => {
    render(<SubmitConversationScreen {...props} />);
    fireEvent.changeText(transcriptInput(), 'me: hi\nwho knows\nbob: hey');
    fireEvent.press(screen.getByText('Get feedback'));
    expect(screen.getAllByText(/Line 2 is missing a "me:" or "them:" prefix\./).length).toBeGreaterThan(0);
    expect(mocked.submitConversation).not.toHaveBeenCalled();
  });

  it('submits trimmed fields with defaults and navigates to the result', async () => {
    mocked.submitConversation.mockResolvedValue({
      id: 'a1',
      conversation_id: 'conv1',
      status: 'pending',
      model_version: 'v1',
      segments: null,
      overall: null,
      created_at: '2030-01-01T00:00:00Z',
    });
    render(<SubmitConversationScreen {...props} />);
    fireEvent.changeText(screen.getByPlaceholderText('Sourdough match'), '  ');
    fireEvent.changeText(screen.getByDisplayValue('hinge'), ' Hinge ');
    fireEvent.changeText(screen.getByPlaceholderText('optional'), ' Sam ');
    fireEvent.changeText(transcriptInput(), 'me: hi\nthem: hey');
    fireEvent.press(screen.getByText('Get feedback'));

    await waitFor(() => expect(navigation.navigate).toHaveBeenCalledWith('Result', { analysisID: 'a1', conversationID: 'conv1' }));
    expect(mocked.submitConversation).toHaveBeenCalledWith({
      title: 'Untitled conversation',
      platform: 'Hinge',
      match_name: 'Sam',
      messages: [
        { sender: 'self', body: 'hi' },
        { sender: 'match', body: 'hey' },
      ],
    });
  });

  it('shows the API error and stays on the screen when submit fails', async () => {
    mocked.submitConversation.mockRejectedValue(new Error('analyzer down'));
    render(<SubmitConversationScreen {...props} />);
    fireEvent.changeText(transcriptInput(), 'me: hi');
    fireEvent.press(screen.getByText('Get feedback'));
    await screen.findByText('analyzer down');
    expect(navigation.navigate).not.toHaveBeenCalled();
  });

  it('"Use the example" fills the transcript and updates the parsed counts', () => {
    render(<SubmitConversationScreen {...props} />);
    fireEvent.press(screen.getByText('Use the example'));
    expect(transcriptInput().props.value).toMatch(/^me: your profile says you bake/);
    expect(screen.getByText('3')).toBeTruthy();
    expect(screen.getByText('2')).toBeTruthy();
    expect(screen.getByText('1')).toBeTruthy();
  });

  it('"Open" navigates to History', () => {
    render(<SubmitConversationScreen {...props} />);
    fireEvent.press(screen.getByText('Open'));
    expect(navigation.navigate).toHaveBeenCalledWith('History');
  });
});
