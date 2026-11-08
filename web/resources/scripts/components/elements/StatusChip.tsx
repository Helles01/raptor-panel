import React from 'react';
import tw, { TwStyle } from 'twin.macro';

export type Tone = 'good' | 'warn' | 'bad' | 'neutral';

const tones: Record<Tone, TwStyle> = {
    good: tw`bg-green-900/60 text-green-300`,
    warn: tw`bg-yellow-900/60 text-yellow-300`,
    bad: tw`bg-red-900/60 text-red-300`,
    neutral: tw`bg-neutral-700 text-neutral-300`,
};

// A short word for a state. The colour says what kind of state it is, so it is never the only thing that does.
const StatusChip = ({ tone = 'neutral', children }: { tone?: Tone; children: React.ReactNode }) => (
    <span css={[tw`inline-flex items-center flex-shrink-0 rounded-md px-2 py-0.5 text-xs font-medium`, tones[tone]]}>
        {children}
    </span>
);

export default StatusChip;
