import PageContentBlock, { PageContentBlockProps } from '@/components/elements/PageContentBlock';
import React from 'react';
import { ServerContext } from '@/state/server';

interface Props extends PageContentBlockProps {
    title: string;
    // For a page that draws its own header, like the console.
    noHeading?: boolean;
}

const ServerContentBlock: React.FC<Props> = ({ title, noHeading, children, ...props }) => {
    const name = ServerContext.useStoreState((state) => state.server.data!.name);

    return (
        <PageContentBlock title={`${name} | ${title}`} heading={noHeading ? undefined : title} {...props}>
            {children}
        </PageContentBlock>
    );
};

export default ServerContentBlock;
