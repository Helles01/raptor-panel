import React, { forwardRef } from 'react';
import { Form } from 'formik';
import styled from 'styled-components/macro';
import { breakpoint } from '@/theme';
import FlashMessageRender from '@/components/FlashMessageRender';
import tw from 'twin.macro';
import Logo from '@/components/elements/Logo';
import { useStoreState } from 'easy-peasy';
import { ApplicationStore } from '@/state';

type Props = React.DetailedHTMLProps<React.FormHTMLAttributes<HTMLFormElement>, HTMLFormElement> & {
    title?: string;
};

const Container = styled.div`
    ${tw`mx-auto w-full px-4`};
    max-width: 26rem;

    ${breakpoint('md')`
        ${tw`pt-10`}
    `};
`;

export default forwardRef<HTMLFormElement, Props>(({ title, ...props }, ref) => {
    const name = useStoreState((state: ApplicationStore) => state.settings.data?.name);

    return (
        <Container>
            <div css={tw`flex items-center mb-8 text-neutral-100`}>
                <Logo size={22} css={tw`text-ember-500 mr-2`} />
                <span css={tw`text-lg font-semibold tracking-tight`}>{name || 'Raptor'}</span>
            </div>
            {title && <h2 css={tw`text-2xl text-neutral-100 font-semibold mb-4`}>{title}</h2>}
            <FlashMessageRender css={tw`mb-3`} />
            <Form {...props} ref={ref}>
                <div css={tw`w-full bg-surface border border-line rounded-lg p-6`}>{props.children}</div>
            </Form>
            <p css={tw`text-xs text-neutral-500 mt-4`}>Raptor Panel</p>
        </Container>
    );
});
