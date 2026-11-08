import React from 'react';
import tw from 'twin.macro';
import { FontAwesomeIcon } from '@fortawesome/react-fontawesome';
import { IconDefinition } from '@fortawesome/free-solid-svg-icons';

// What a list says when it has nothing in it: a box of its own, so the page does not look broken.
const EmptyState = ({ icon, title, children }: { icon: IconDefinition; title: string; children?: React.ReactNode }) => (
    <div css={tw`rounded-xl border border-dashed border-neutral-600 px-6 py-10 text-center`}>
        <div css={tw`w-10 h-10 mx-auto flex items-center justify-center rounded-lg bg-neutral-700 text-neutral-400`}>
            <FontAwesomeIcon icon={icon} />
        </div>
        <p css={tw`mt-4 font-medium text-neutral-100`}>{title}</p>
        {children && <p css={tw`mt-1 text-sm text-neutral-400`}>{children}</p>}
    </div>
);

export default EmptyState;
